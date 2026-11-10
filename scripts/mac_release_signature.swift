// Read-only signature observation for the fixed macOS release recipe.
// Compile with Security, CryptoKit and LightweightCodeRequirements on macOS.
// This never signs, launches inspected code, or accesses a signing identity.
// Apple API references:
// https://developer.apple.com/documentation/lightweightcoderequirements/ondiskcoderequirement
// https://developer.apple.com/documentation/lightweightcoderequirements/validationresult
// https://developer.apple.com/documentation/security/signing-information-dictionary-keys
import Foundation
import Security
import CryptoKit
import LightweightCodeRequirements

private struct Refusal: Error {
    let reason: String
    init(_ reason: String) { self.reason = reason }
}

private struct Request {
    let path: URL
    let team: String
    let identifier: String
    let certificate: String
    let expectedHash: String?

    init(_ arguments: [String]) throws {
        let required = Set(["--path", "--team-id", "--identifier", "--certificate-sha1"])
        guard arguments.count == 8 || arguments.count == 10 else { throw Refusal("invalid-arguments") }
        var fields: [String: String] = [:]
        for offset in stride(from: 0, to: arguments.count, by: 2) {
            let key = arguments[offset]
            guard required.contains(key) || key == "--cdhash", fields[key] == nil else {
                throw Refusal("invalid-arguments")
            }
            fields[key] = arguments[offset + 1]
        }
        guard required.isSubset(of: Set(fields.keys)),
              let rawPath = fields["--path"], rawPath.hasPrefix("/"),
              let team = fields["--team-id"], team.utf8.count == 10, Self.matches(team, "^[A-Z0-9]{10}$"),
              let identifier = fields["--identifier"],
              let certificate = fields["--certificate-sha1"],
              certificate.utf8.count == 40, Self.matches(certificate, "^[A-F0-9]{40}$"), certificate.contains(where: { $0 != "0" }) else {
            throw Refusal("invalid-arguments")
        }
        let url = URL(fileURLWithPath: rawPath)
        guard url.standardizedFileURL.path == rawPath,
              url.resolvingSymlinksInPath().path == rawPath else { throw Refusal("noncanonical-path") }
        let names = [
            "com.o2connective.rescue-host.resident-pipe": "resident-pipe",
            "com.o2connective.rescue-host.resident-preparation": "libResidentPreparation.dylib",
            "com.o2connective.rescue-host.shell": "app"
        ]
        guard let name = names[identifier],
              (identifier == "com.o2connective.rescue-host.shell"
               ? url.pathExtension == name : url.lastPathComponent == name) else {
            throw Refusal("unexpected-code-role")
        }
        var directory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: rawPath, isDirectory: &directory),
              directory.boolValue == (identifier == "com.o2connective.rescue-host.shell") else {
            throw Refusal("missing-code")
        }
        if let hash = fields["--cdhash"] {
            guard hash.utf8.count == 40, Self.matches(hash, "^[a-f0-9]{40}$"), hash.contains(where: { $0 != "0" }) else {
                throw Refusal("invalid-cdhash")
            }
        }
        self.path = url
        self.team = team
        self.identifier = identifier
        self.certificate = certificate
        self.expectedHash = fields["--cdhash"]
    }

    private static func matches(_ value: String, _ pattern: String) -> Bool {
        value.range(of: pattern, options: .regularExpression) != nil
    }
}

@available(macOS 14.4, *)
private func observe(_ request: Request) throws -> [String: Any] {
    var staticCode: SecStaticCode?
    guard SecStaticCodeCreateWithPath(request.path as CFURL, [], &staticCode) == errSecSuccess,
          let code = staticCode else { throw Refusal("static-code-unavailable") }
    let flags = SecCSFlags(rawValue: kSecCSStrictValidate | kSecCSCheckAllArchitectures)
    let identity = try OnDiskCodeRequirement.allOf {
        TeamIdentifier(request.team)
        SigningIdentifier(request.identifier)
        ValidationCategory(.developerID)
    }
    let initial = SecStaticCodeCheckValidityWithOnDiskRequirement(code: code, flags: flags, requirement: identity)
    guard initial.signatureIsValid, initial.requirementMatched else { throw Refusal("signature-or-identity-invalid") }

    var information: CFDictionary?
    guard SecCodeCopySigningInformation(code, SecCSFlags(rawValue: kSecCSSigningInformation), &information) == errSecSuccess,
          let info = information as? [String: Any],
          info[kSecCodeInfoIdentifier as String] as? String == request.identifier,
          info[kSecCodeInfoTeamIdentifier as String] as? String == request.team,
          let hash = info[kSecCodeInfoUnique as String] as? Data,
          hash.count == 20, hash.contains(where: { $0 != 0 }),
          let signatureFlags = info[kSecCodeInfoFlags as String] as? NSNumber,
          signatureFlags.uint32Value & SecCodeSignatureFlags.runtime.rawValue != 0,
          info[kSecCodeInfoTimestamp as String] is Date,
          let certificates = info[kSecCodeInfoCertificates as String] as? [SecCertificate],
          let leaf = certificates.first else { throw Refusal("incomplete-signing-metadata") }
    // SHA-1 here identifies the explicitly selected certificate, not code integrity.
    let certificate = Insecure.SHA1.hash(data: SecCertificateCopyData(leaf) as Data)
        .map { String(format: "%02X", $0) }.joined()
    guard certificate == request.certificate else { throw Refusal("certificate-mismatch") }
    // Reject even an unparsed entitlement blob rather than treating a missing
    // dictionary as proof that no entitlements exist (SecCode.h documents this).
    if let entitlements = info[kSecCodeInfoEntitlementsDict as String] {
        guard let dictionary = entitlements as? [String: Any], dictionary.isEmpty else {
            throw Refusal("unauthorized-entitlements")
        }
    } else if info[kSecCodeInfoEntitlements as String] != nil {
        throw Refusal("unparsed-entitlements")
    }
    let cdhash = hash.map { String(format: "%02x", $0) }.joined()
    guard request.expectedHash == nil || request.expectedHash == cdhash else { throw Refusal("cdhash-mismatch") }
    // Re-evaluate the complete typed identity plus genuine CodeDirectory hash.
    let exact = try OnDiskCodeRequirement.allOf {
        TeamIdentifier(request.team)
        SigningIdentifier(request.identifier)
        ValidationCategory(.developerID)
        CodeDirectoryHash.in([hash])
    }
    let final = SecStaticCodeCheckValidityWithOnDiskRequirement(code: code, flags: flags, requirement: exact)
    guard final.signatureIsValid, final.requirementMatched else { throw Refusal("exact-requirement-invalid") }
    return ["schema": "tether-mac-release-signature/1", "signingIdentifier": request.identifier,
            "teamIdentifier": request.team, "certificateSha1": certificate, "cdhash": cdhash,
            "validationCategory": "developer-id", "secureTimestamp": true,
            "hardenedRuntime": true, "entitlements": [String: String]()]
}

do {
    let request = try Request(Array(CommandLine.arguments.dropFirst()))
    #if arch(arm64)
    guard ProcessInfo.processInfo.operatingSystemVersion.majorVersion >= 27 else { throw Refusal("macos-27-required") }
    if #available(macOS 14.4, *) {
        let output = try JSONSerialization.data(withJSONObject: observe(request), options: [.sortedKeys])
        FileHandle.standardOutput.write(output)
        FileHandle.standardOutput.write(Data([10]))
    } else { throw Refusal("unsupported-os") }
    #else
    throw Refusal("native-arm64-required")
    #endif
} catch {
    // No target paths, certificates, or OS diagnostic strings in failure output.
    let reason = (error as? Refusal)?.reason ?? "signature-observation-failed"
    FileHandle.standardError.write(Data(("mac-release-signature: " + reason + "\n").utf8))
    exit(2)
}
