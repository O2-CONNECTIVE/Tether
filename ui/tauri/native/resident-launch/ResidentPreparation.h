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
#endif
#endif
