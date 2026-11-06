#ifndef RESIDENT_PREPARATION_H
#define RESIDENT_PREPARATION_H
#include <stdint.h>

/* Internal, unlinked preparation ABI. No launch or exit permission. */
enum {
    RH_PREPARATION_INVALID = 0,
    RH_PREPARATION_OWNED_UNLAUNCHED = 1,
    RH_PREPARATION_RETIRED_UNLAUNCHED = 2,
    RH_PREPARATION_UNKNOWN = 3,
    RH_PREPARATION_WRONG_THREAD = 4
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

#ifdef RESIDENT_LAUNCH_FIXTURE
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
#endif
#endif
