import Darwin
import Foundation

private final class WorkerIdentity {}
private enum PreparationState: UInt32 {
    case invalid = 0, ownedUnlaunched = 1, retiredUnlaunched = 2, unknown = 3, wrongThread = 4, ownedActive = 5, activeIORetired = 6
}

// The only unchecked sharing is this bounded slot. Every field access is under
// lock; Process/IO methods run only on the creator's still-live TLS identity.
private final class PreparationSlot: @unchecked Sendable {
    private struct Record {
        let handle: UInt64
        let worker: WorkerIdentity
        let owner: ResidentLaunchPreparation
        var unknown = false
        var active = false
        var activeIORetired = false
    }
    private let lock = NSLock()
    private let key: pthread_key_t?
    private var issued: UInt64 = 0
    private var record: Record?
    #if RESIDENT_LAUNCH_FIXTURE
    private var completedCloses: UInt32 = 0
    private var retirementCalls: UInt32 = 0
    private var activeAbandons: UInt32 = 0
    #endif

    init() {
        var created = pthread_key_t()
        let result = pthread_key_create(&created) { pointer in
            let worker = Unmanaged<WorkerIdentity>.fromOpaque(pointer).takeRetainedValue()
            preparationSlot.workerExited(worker)
        }
        key = result == 0 ? created : nil
    }

    private func worker(create: Bool) -> WorkerIdentity? {
        guard let key else { return nil }
        if let pointer = pthread_getspecific(key) {
            return Unmanaged<WorkerIdentity>.fromOpaque(pointer).takeUnretainedValue()
        }
        guard create else { return nil }
        let identity = WorkerIdentity()
        let retained = Unmanaged.passRetained(identity)
        guard pthread_setspecific(key, retained.toOpaque()) == 0 else {
            retained.release()
            return nil
        }
        return identity
    }

    func create(fixtureMode: UInt32?) -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        guard record == nil, issued < UInt64.max else { return 0 }
        let owner: ResidentLaunchPreparation
        do {
            #if RESIDENT_LAUNCH_FIXTURE
            if let mode = fixtureMode {
                guard mode <= 3 else { return 0 }
                owner = try ResidentLaunchPreparation.fixture(team: "SYNTHETIC1", hashes: [Data(repeating: 7, count: 20)],
                    version: ProcessInfo.processInfo.operatingSystemVersion)
            } else { owner = try ResidentLaunchPreparation.prepare() }
            #else
            owner = try ResidentLaunchPreparation.prepare()
            #endif
        } catch { return 0 }
        guard let identity = worker(create: true) else { return 0 }
        issued += 1
        let handle = issued
        record = Record(handle: handle, worker: identity, owner: owner)
        do {
            #if RESIDENT_LAUNCH_FIXTURE
            if fixtureMode == 1 || fixtureMode == 3 { owner.loseCloseAcknowledgement() }
            try owner.prepareIO(partial: fixtureMode == 2 || fixtureMode == 3)
            #else
            try owner.prepareIO(partial: false)
            #endif
            return handle
        } catch {
            if owner.retireIO() { discardRecord(); return 0 }
            record?.unknown = true
            return handle // Caller must inspect UNKNOWN, never a prepared permit.
        }
    }

    private func stateLocked(_ handle: UInt64) -> PreparationState {
        guard let record, handle != 0, record.handle == handle else { return .invalid }
        if record.unknown { return .unknown }
        guard worker(create: false) === record.worker else { return .wrongThread }
        if record.active { return record.activeIORetired ? .activeIORetired : .ownedActive }
        return .ownedUnlaunched
    }

    func state(_ handle: UInt64, retire: Bool) -> PreparationState {
        lock.lock()
        defer { lock.unlock() }
        #if RESIDENT_LAUNCH_FIXTURE
        if retire { retirementCalls += 1 }
        #endif
        let state = stateLocked(handle)
        guard state == .ownedUnlaunched, retire, let owner = record?.owner else { return state }
        guard owner.retireIO() else { record?.unknown = true; return .unknown }
        discardRecord()
        return .retiredUnlaunched
    }

    private func discardRecord() {
        #if RESIDENT_LAUNCH_FIXTURE
        completedCloses += record?.owner.fixtureCounts().closed ?? 0
        #endif
        record = nil
    }

    func bytes(_ handle: UInt64, _ operation: PreparedByteOperation) -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        let state = stateLocked(handle)
        guard (state == .ownedUnlaunched || state == .ownedActive), let owner = record?.owner else {
            return PreparedByteResult(tag: .rejected).packed
        }
        let result = owner.bytes(operation)
        if result.tag == .unknown {
            if record?.active != true { _ = owner.retireIO() } // Active Wait is separate.
            record?.unknown = true
        }
        return result.packed
    }

    private func workerExited(_ identity: WorkerIdentity) {
        lock.lock()
        defer { lock.unlock() }
        guard record?.worker === identity else { return }
        // Destructor still runs on the original thread. Never touch another
        // thread's owner. Loss of its controller remains unknown even if these
        // unlaunched pipes close; never discard/replace the Process record.
        if record?.active == true { _ = abandonActiveLocked(); return }
        if record?.unknown == false { _ = record?.owner.retireIO() }
        record?.unknown = true
    }

    // Every active operation checks the original TLS identity BEFORE touching
    // Process/IO, including when the record is already unknown.
    func completion(_ handle: UInt64) -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        guard let record, handle != 0, record.handle == handle else { return 0 }
        guard worker(create: false) === record.worker else { return 5 << 32 }
        guard record.active else { return 0 }
        let observation = record.owner.pollCompletion()
        if case .unknown = observation { self.record?.unknown = true }
        if case .unavailable = observation { self.record?.unknown = true; return 4 << 32 }
        return packed(observation)
    }

    private func packed(_ observation: ProcessCompletionObservation) -> UInt64 {
        switch observation {
        case .unavailable: return 0
        case .pending: return 1 << 32
        case .unknown: return 4 << 32
        case let .observed(exit):
            let tag: UInt64 = exit.reason == .exit ? 2 : 3
            return tag << 32 | UInt64(UInt32(bitPattern: exit.status))
        }
    }

    func retireActiveIO(_ handle: UInt64) -> PreparationState {
        lock.lock()
        defer { lock.unlock() }
        guard let record, handle != 0, record.handle == handle else { return .invalid }
        guard worker(create: false) === record.worker else { return .wrongThread }
        guard record.active else { return stateLocked(handle) }
        guard record.owner.hasObservedExit else { return stateLocked(handle) }
        if record.owner.retireIO() { self.record?.activeIORetired = true }
        else { self.record?.unknown = true }
        // Keep the original record even on success. No full-join/replacement
        // permission exists yet. This is an IO component observation only.
        return stateLocked(handle)
    }

    func abandonActive(_ handle: UInt64) -> PreparationState {
        lock.lock()
        defer { lock.unlock() }
        guard let record, handle != 0, record.handle == handle else { return .invalid }
        guard worker(create: false) === record.worker else { return .wrongThread }
        guard record.active else { return stateLocked(handle) }
        return abandonActiveLocked()
    }

    private func abandonActiveLocked() -> PreparationState {
        guard let owner = record?.owner else { return .invalid }
        #if RESIDENT_LAUNCH_FIXTURE
        activeAbandons += 1
        #endif
        _ = owner.bytes(.halfClose) // Original writer, never kill/replace/retry close.
        if case .observed = owner.pollCompletion() { _ = owner.retireIO() }
        record?.unknown = true // Loss of controller is not completion permission.
        return .unknown
    }

    #if RESIDENT_LAUNCH_FIXTURE
    func createActive(_ mode: UInt32) -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        guard mode <= 4, record == nil, issued < UInt64.max,
              let identity = worker(create: true) else { return 0 }
        let owner = ResidentLaunchPreparation.completionFixture()
        issued += 1
        record = Record(handle: issued, worker: identity, owner: owner, active: true)
        if mode == 2 { owner.loseCloseAcknowledgement() }
        do {
            if try !owner.runOwnedIOFixture(mode == 0 ? .echo : mode == 3 ? .exitTwo : mode == 4 ? .signal : .early) { record?.unknown = true }
        } catch { record?.unknown = true }
        return issued
    }
    func collectActiveFixture() -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        guard let record, record.active, record.unknown,
              worker(create: false) === record.worker else { return 0 }
        let observation = record.owner.pollCompletion()
        if case .observed = observation { _ = record.owner.retireIO() }
        // Only same retained record cleanup; unknown never clears.
        return packed(observation)
    }
    func retainedState() -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        return record.map { stateLocked($0.handle).rawValue } ?? 0
    }
    func activeStats() -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        return UInt64(record?.owner.completionFixtureWaits() ?? 0) << 32 | UInt64(activeAbandons)
    }
    func peer(_ handle: UInt64, action: UInt32) -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        guard stateLocked(handle) == .ownedUnlaunched, let owner = record?.owner else { return 0 }
        return owner.peer(action) ? 1 : 0
    }
    func sendPeer(_ handle: UInt64, pointer: UnsafePointer<UInt8>?, count: UInt32) -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        guard stateLocked(handle) == .ownedUnlaunched, let owner = record?.owner else { return 0 }
        return owner.sendPeer(pointer, count: count)
    }
    func receivePeer(_ handle: UInt64, pointer: UnsafeMutablePointer<UInt8>?, count: UInt32) -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        guard stateLocked(handle) == .ownedUnlaunched, let owner = record?.owner else { return 0 }
        return owner.receivePeer(pointer, count: count)
    }
    func probe(_ handle: UInt64, exchange: Bool) -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        guard stateLocked(handle) == .ownedUnlaunched, let owner = record?.owner else { return 0 }
        if exchange { return owner.exchange() ? 1 : 0 }
        guard #available(macOS 14.4, *), let snapshot = try? owner.snapshot() else { return 0 }
        var result: UInt32 = 0
        if !snapshot.running { result |= 1 }
        if !snapshot.hasExecutable { result |= 2 }
        if !snapshot.hasArguments { result |= 4 }
        if !snapshot.retained.isEmpty { result |= 8 }
        if owner.ioIntact() { result |= 16 }
        return result
    }
    func counts(open: Bool) -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        let counts = record?.owner.fixtureCounts() ?? (open: 0, closed: 0)
        return open ? counts.open : completedCloses + counts.closed
    }
    func retireCalls() -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        return retirementCalls
    }
    func exhaust() -> UInt32 {
        lock.lock()
        defer { lock.unlock() }
        guard record == nil, issued == 0 else { return 0 }
        issued = UInt64.max - 1
        return 1
    }
    #endif
}

private let preparationSlot = PreparationSlot()

@c @implementation public func rh_resident_preparation_create() -> UInt64 {
    autoreleasepool { preparationSlot.create(fixtureMode: nil) }
}
@c @implementation public func rh_resident_preparation_state(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.state(handle, retire: false).rawValue }
}
@c @implementation public func rh_resident_preparation_retire(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.state(handle, retire: true).rawValue }
}
@c @implementation public func rh_resident_preparation_read(_ handle: UInt64, _ bytes: UnsafeMutablePointer<UInt8>?, _ count: UInt32) -> UInt64 {
    autoreleasepool { preparationSlot.bytes(handle, .read(bytes, count)) }
}
@c @implementation public func rh_resident_preparation_write(_ handle: UInt64, _ bytes: UnsafePointer<UInt8>?, _ count: UInt32) -> UInt64 {
    autoreleasepool { preparationSlot.bytes(handle, .write(bytes, count)) }
}
@c @implementation public func rh_resident_preparation_half_close(_ handle: UInt64) -> UInt64 {
    autoreleasepool { preparationSlot.bytes(handle, .halfClose) }
}

@c @implementation public func rh_resident_preparation_exit(_ handle: UInt64) -> UInt64 {
    autoreleasepool { preparationSlot.completion(handle) }
}
@c @implementation public func rh_resident_preparation_retire_active_io(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.retireActiveIO(handle).rawValue }
}
@c @implementation public func rh_resident_preparation_abandon_active(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.abandonActive(handle).rawValue }
}

#if RESIDENT_LAUNCH_FIXTURE
@c @implementation public func rh_fixture_preparation_create_active(_ mode: UInt32) -> UInt64 {
    autoreleasepool { preparationSlot.createActive(mode) }
}
@c @implementation public func rh_fixture_preparation_collect_active() -> UInt64 {
    autoreleasepool { preparationSlot.collectActiveFixture() }
}
@c @implementation public func rh_fixture_preparation_active_stats() -> UInt64 { preparationSlot.activeStats() }
@c @implementation public func rh_fixture_preparation_retained_state() -> UInt32 { preparationSlot.retainedState() }
@c @implementation public func rh_fixture_preparation_peer(_ handle: UInt64, _ action: UInt32) -> UInt32 {
    autoreleasepool { preparationSlot.peer(handle, action: action) }
}
@c @implementation public func rh_fixture_preparation_send(_ handle: UInt64, _ bytes: UnsafePointer<UInt8>?, _ count: UInt32) -> UInt32 {
    autoreleasepool { preparationSlot.sendPeer(handle, pointer: bytes, count: count) }
}
@c @implementation public func rh_fixture_preparation_receive(_ handle: UInt64, _ bytes: UnsafeMutablePointer<UInt8>?, _ count: UInt32) -> UInt32 {
    autoreleasepool { preparationSlot.receivePeer(handle, pointer: bytes, count: count) }
}
@c @implementation public func rh_fixture_preparation_create(_ mode: UInt32) -> UInt64 {
    autoreleasepool { preparationSlot.create(fixtureMode: mode) }
}
@c @implementation public func rh_fixture_preparation_probe(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.probe(handle, exchange: false) }
}
@c @implementation public func rh_fixture_preparation_exchange(_ handle: UInt64) -> UInt32 {
    autoreleasepool { preparationSlot.probe(handle, exchange: true) }
}
@c @implementation public func rh_fixture_preparation_close_count() -> UInt32 { preparationSlot.counts(open: false) }
@c @implementation public func rh_fixture_preparation_open_count() -> UInt32 { preparationSlot.counts(open: true) }
@c @implementation public func rh_fixture_preparation_exhaust() -> UInt32 { preparationSlot.exhaust() }
@c @implementation public func rh_fixture_preparation_retire_calls() -> UInt32 { preparationSlot.retireCalls() }
#endif
