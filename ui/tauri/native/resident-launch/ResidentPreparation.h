#ifndef RESIDENT_PREPARATION_H
#define RESIDENT_PREPARATION_H
#include <stdint.h>

/* Internal, unlinked preparation ABI. No launch or exit permission. */
enum {
    RH_PREPARATION_INVALID = 0,
    RH_PREPARATION_OWNED_UNLAUNCHED = 1,
    RH_PREPARATION_RETIRED_UNLAUNCHED = 2,
    RH_PREPARATION_UNKNOWN = 3,
    RH_PREPARATION_WRONG_THREAD = 4,
    RH_PREPARATION_OWNED_ACTIVE = 5,
    RH_PREPARATION_ACTIVE_IO_RETIRED = 6
};

/* 0: refused, no returned ownership. Nonzero: inspect state, retain handle.
 * UNKNOWN retains the same owner and blocks replacement. Never retry close.
 * These are process-local ownership handles, not authorization credentials. */
uint64_t rh_resident_preparation_create(void);
uint32_t rh_resident_preparation_state(uint64_t handle);
uint32_t rh_resident_preparation_retire(uint64_t handle);
/* Borrowed bytes only; owner retains every fd. Null/zero/excessive counts are
 * rejected. Non-null buffers must be live and large enough for count (1..4096).
 * No pointer is retained. Result: tag in high32,
 * transferred count in low32. Tags: rejected0 bytes1 pending2 rawEOF3
 * halfClosed4 unknown5. EOF/halfClosed are never exit/Close/Wait proofs. */
uint64_t rh_resident_preparation_read(uint64_t handle, uint8_t * _Nullable bytes, uint32_t count);
uint64_t rh_resident_preparation_write(uint64_t handle, const uint8_t * _Nullable bytes, uint32_t count);
uint64_t rh_resident_preparation_half_close(uint64_t handle);

/* No launch API. Same-worker completion observation: high32 tag unavailable0,
 * pending1, exit2, signal3, unknown4, wrong-thread5; low32 signed status bits
 * only for exit/signal. Not EOF, IO cleanup or full joins. */
uint64_t rh_resident_preparation_exit(uint64_t handle);
/* Requires actual observed same-Process Wait. IO retirement additionally needs
 * EOF, every original close receipt, no IO fault. Retains native record even
 * on ACTIVE_IO_RETIRED; no replacement/full-join permission. */
uint32_t rh_resident_preparation_retire_active_io(uint64_t handle);
/* Controller abandonment keeps original record UNKNOWN. Only original writer
 * half-close and nonblocking same-owner collection; no kill or close retry. */
uint32_t rh_resident_preparation_abandon_active(uint64_t handle);

/* Explicit shutdown preserves observation until raw EOF, even after exit.
 * Discard: rejected0 progress1 pending2 rawEOF3 error4. One nonblocking read
 * into fixed private 4096-byte storage; no payload/control/cleanup permission.
 * Requires same active, unretired handle/TLS worker after shutdown request. */
uint32_t rh_resident_preparation_request_active_close(uint64_t handle);
uint32_t rh_resident_preparation_discard_observation(uint64_t handle);

#ifdef RESIDENT_LAUNCH_FIXTURE
/* Separate untrusted self-child owner. mode0 echo,1 early exit,2 early lost ack,3 exit2,4 self SIGTERM,
 * 5/6 finite malformed output,7 finite output with lost close receipt.
 * Not callable from a requirement-bearing production preparation. */
uint64_t rh_fixture_preparation_create_active(uint32_t mode);
/* Same original worker/record collection after incomplete fixture Drop only. */
uint64_t rh_fixture_preparation_collect_active(void);
/* high32 actual same-Process Wait calls, low32 active abandonment calls. */
uint64_t rh_fixture_preparation_active_stats(void);
uint32_t rh_fixture_preparation_retained_state(void);

/* Test compilation only: fixed synthetic identity, no caller-supplied release.
 * mode 0 normal, 1 lost close acknowledgement, 2 partial pipe acquisition,
 * 3 lost acknowledgement after partial acquisition. */
uint64_t rh_fixture_preparation_create(uint32_t mode);
uint32_t rh_fixture_preparation_probe(uint64_t handle);
uint32_t rh_fixture_preparation_exchange(uint64_t handle);
uint32_t rh_fixture_preparation_close_count(void);
uint32_t rh_fixture_preparation_open_count(void);
uint32_t rh_fixture_preparation_exhaust(void);
uint32_t rh_fixture_preparation_retire_calls(void);
uint32_t rh_fixture_preparation_peer(uint64_t handle, uint32_t action);
uint32_t rh_fixture_preparation_send(uint64_t handle, const uint8_t * _Nullable bytes, uint32_t count);
uint32_t rh_fixture_preparation_receive(uint64_t handle, uint8_t * _Nullable bytes, uint32_t count);
#endif
#endif
