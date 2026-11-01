package macagent

import "testing"

type fakeGroupQuery struct {
	list         groupListing
	info         groupMember
	err          groupQueryError
	lists, infos int
}

func (f *fakeGroupQuery) listGroup(pgid int) groupListing {
	if pgid != 77 {
		panic("unexpected group")
	}
	f.lists++
	return f.list
}
func (f *fakeGroupQuery) member(pid int) (groupMember, groupQueryError) {
	if pid != 77 {
		panic("unexpected member")
	}
	f.infos++
	return f.info, f.err
}
func TestGroupObserverFakeCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ids    []int
		le, me groupQueryError
		m      groupMember
		want   groupObservation
	}{
		{"anchor", []int{77}, 0, 0, groupMember{77, 77, true}, groupAnchorOnly},
		{"empty", nil, 0, 0, groupMember{}, groupMissingAnchor},
		{"permission", nil, groupQueryPermission, 0, groupMember{}, groupPermission},
		{"query-error", nil, groupQueryOther, 0, groupMember{}, groupQueryFailed},
		{"list-race", nil, groupQueryMissing, 0, groupMember{}, groupRace},
		{"missing-anchor", []int{78}, 0, 0, groupMember{}, groupMissingAnchor},
		{"duplicate", []int{77, 77}, 0, 0, groupMember{}, groupInvalidList},
		{"invalid-pid", []int{77, 0}, 0, 0, groupMember{}, groupInvalidList},
		{"multiple", []int{77, 78}, 0, 0, groupMember{}, groupMultiple},
		{"member-permission", []int{77}, 0, groupQueryPermission, groupMember{}, groupPermission},
		{"member-partial", []int{77}, 0, groupQueryOther, groupMember{}, groupQueryFailed},
		{"member-vanished", []int{77}, 0, groupQueryMissing, groupMember{}, groupRace},
		{"pid-changed", []int{77}, 0, 0, groupMember{78, 77, true}, groupChanged},
		{"pgid-changed", []int{77}, 0, 0, groupMember{77, 78, true}, groupChanged},
		{"live", []int{77}, 0, 0, groupMember{77, 77, false}, groupLive},
		{"full-buffer", make([]int, groupObservationLimit), 0, 0, groupMember{}, groupTruncated},
		{"oversize", make([]int, groupObservationLimit+1), 0, 0, groupMember{}, groupTruncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGroupQuery{list: groupListing{tc.ids, tc.le}, info: tc.m, err: tc.me}
			got := observeOwnedGroup(groupAnchor{77, true, true}, f)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if f.lists != 1 || f.infos > 1 {
				t.Fatal("unbounded query")
			}
			if len(tc.ids) != 1 && f.infos != 0 {
				t.Fatal("multiple members must not be probed")
			}
		})
	}
}
func TestGroupObserverInvalidAnchorDoesNotQuery(t *testing.T) {
	for _, a := range []groupAnchor{{}, {1, true, true}, {77, false, true}, {77, true, false}} {
		f := &fakeGroupQuery{}
		if observeOwnedGroup(a, f) != groupInvalidAnchor || f.lists != 0 || f.infos != 0 {
			t.Fatal("invalid anchor queried")
		}
	}
	if observeOwnedGroup(groupAnchor{77, true, true}, nil) != groupInvalidAnchor {
		t.Fatal("nil query")
	}
}
func TestGroupObserverSnapshotIsNotPersistent(t *testing.T) {
	f := &fakeGroupQuery{list: groupListing{pids: []int{77}}, info: groupMember{77, 77, true}}
	a := groupAnchor{77, true, true}
	if observeOwnedGroup(a, f) != groupAnchorOnly {
		t.Fatal("initial snapshot")
	}
	// A previously escaped member can rejoin. No result is cached as cleanup.
	f.list.pids = []int{77, 78}
	if observeOwnedGroup(a, f) != groupMultiple {
		t.Fatal("must not persist earlier observation")
	}
}
