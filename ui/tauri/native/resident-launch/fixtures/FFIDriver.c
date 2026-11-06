#include "../ResidentPreparation.h"
#include <pthread.h>
#include <stdio.h>
#include <string.h>

static unsigned passed, failed;
static uint64_t handle;
static void check(int value) { if (value) ++passed; else ++failed; }

static void *wrong_thread(void *unused) {
    (void)unused;
    check(rh_resident_preparation_state(handle) == RH_PREPARATION_WRONG_THREAD);
    check(rh_resident_preparation_retire(handle) == RH_PREPARATION_WRONG_THREAD);
    return NULL;
}

static void *abandon_owner(void *unused) {
    (void)unused;
    handle = rh_fixture_preparation_create(0);
    check(handle != 0);
    check(rh_fixture_preparation_probe(handle) == 31);
    return NULL; /* TLS destructor must retain original owner as unknown. */
}

static int joined(void *(*start)(void *)) {
    pthread_t thread;
    if (pthread_create(&thread, NULL, start, NULL) != 0) return 0;
    if (pthread_join(thread, NULL) != 0) {
        /* No further access to shared fixture state after uncertain join.
         * Leave the original worker alive; no complete report means failure. */
        fputs("NATIVE_PREPARATION_FFI_UNEXECUTED join_unknown\n", stderr);
        pthread_exit(NULL);
    }
    return 1;
}

int main(int argc, char **argv) {
    if (argc != 2) return 2;
    check(rh_resident_preparation_create() == 0); /* publisher unset */
    check(rh_resident_preparation_state(0) == RH_PREPARATION_INVALID);
    check(rh_resident_preparation_retire(7) == RH_PREPARATION_INVALID);
    check(rh_fixture_preparation_open_count() == 0);
    if (strcmp(argv[1], "lifetime") == 0) {
        handle = rh_fixture_preparation_create(0);
        check(handle != 0);
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_OWNED_UNLAUNCHED);
        check(rh_fixture_preparation_probe(handle) == 31);
        check(rh_fixture_preparation_create(0) == 0);
        check(joined(wrong_thread));
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_OWNED_UNLAUNCHED);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_RETIRED_UNLAUNCHED);
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_INVALID);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_INVALID);
        uint64_t next = rh_fixture_preparation_create(0);
        check(next > handle);
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_INVALID);
        check(rh_resident_preparation_retire(next) == RH_PREPARATION_RETIRED_UNLAUNCHED);
        check(rh_fixture_preparation_close_count() == 8);
    } else if (strcmp(argv[1], "io") == 0) {
        handle = rh_fixture_preparation_create(0);
        check(handle != 0);
        check(rh_fixture_preparation_exchange(handle) == 1);
        check(rh_fixture_preparation_close_count() == 1);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_RETIRED_UNLAUNCHED);
        check(rh_fixture_preparation_close_count() == 4);
    } else if (strcmp(argv[1], "unknown") == 0) {
        handle = rh_fixture_preparation_create(1);
        check(handle != 0);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_UNKNOWN);
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_UNKNOWN);
        check(rh_fixture_preparation_close_count() == 4);
        check(rh_fixture_preparation_create(0) == 0);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_UNKNOWN);
        check(rh_fixture_preparation_close_count() == 4);
    } else if (strcmp(argv[1], "thread-exit") == 0) {
        check(joined(abandon_owner));
        check(rh_resident_preparation_state(handle) == RH_PREPARATION_UNKNOWN);
        check(rh_fixture_preparation_create(0) == 0);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_UNKNOWN);
        check(rh_fixture_preparation_close_count() == 4);
    } else if (strcmp(argv[1], "partial") == 0) {
        check(rh_fixture_preparation_create(2) == 0);
        check(rh_fixture_preparation_close_count() == 2);
        handle = rh_fixture_preparation_create(0);
        check(handle != 0);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_RETIRED_UNLAUNCHED);
        check(rh_fixture_preparation_close_count() == 6);
    } else if (strcmp(argv[1], "overflow") == 0) {
        check(rh_fixture_preparation_exhaust() == 1);
        handle = rh_fixture_preparation_create(0);
        check(handle == UINT64_MAX);
        check(rh_resident_preparation_retire(handle) == RH_PREPARATION_RETIRED_UNLAUNCHED);
        check(rh_fixture_preparation_create(0) == 0);
        check(rh_fixture_preparation_close_count() == 4);
    } else return 2;
    check(rh_fixture_preparation_open_count() == 0);
    printf("NATIVE_PREPARATION_FFI mode=%s passed=%u failed=%u launched=0\n", argv[1], passed, failed);
    return failed ? 1 : 0;
}
