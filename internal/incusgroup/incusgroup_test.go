package incusgroup

import (
	"errors"
	"strings"
	"testing"
)

// fixedDetector builds a Detector with controllable inputs so each state can be
// constructed deterministically without faking the whole flow.
func fixedDetector(installed bool, gid int, gidExists, member bool, username string, procGroups []int, procErr error) *Detector {
	return &Detector{
		IncusInstalled: func() bool { return installed },
		ResolveGID:     func() (int, bool) { return gid, gidExists },
		UserInGroup:    func(int) (bool, string) { return member, username },
		ProcessGroups:  func() ([]int, error) { return procGroups, procErr },
	}
}

func TestDetect_OK(t *testing.T) {
	d := fixedDetector(true, 994, true, true, "ubuntu", []int{4, 994, 1000}, nil)
	got := d.Detect()
	if got.State != StateOK {
		t.Fatalf("state = %q, want %q (detail=%s)", got.State, StateOK, got.Detail)
	}
	if got.GID != 994 {
		t.Errorf("gid = %d, want 994", got.GID)
	}
	if got.Remedy != "none" {
		t.Errorf("remedy = %q, want none", got.Remedy)
	}
}

func TestDetect_Stale(t *testing.T) {
	// User is a member (member=true) but the process groups do NOT include gid.
	d := fixedDetector(true, 994, true, true, "ubuntu", []int{4, 1000}, nil)
	got := d.Detect()
	if got.State != StateStale {
		t.Fatalf("state = %q, want %q", got.State, StateStale)
	}
	if got.Remedy != "restart-user-manager" {
		t.Errorf("remedy = %q, want restart-user-manager", got.Remedy)
	}
	if !strings.Contains(got.Detail, "user manager") {
		t.Errorf("stale detail should mention the user manager, got: %s", got.Detail)
	}
	if strings.Contains(got.Detail, "systemctl --user restart palmux2") && !strings.Contains(got.Detail, "NOT enough") {
		t.Errorf("stale detail should clarify a plain --user restart is NOT enough")
	}
}

func TestDetect_NotMember(t *testing.T) {
	d := fixedDetector(true, 994, true, false, "ubuntu", []int{4, 1000}, nil)
	got := d.Detect()
	if got.State != StateNotMember {
		t.Fatalf("state = %q, want %q", got.State, StateNotMember)
	}
	if got.Remedy != "usermod" {
		t.Errorf("remedy = %q, want usermod", got.Remedy)
	}
	if !strings.Contains(got.Detail, "usermod -aG incus-admin") {
		t.Errorf("not-member detail should give the usermod command, got: %s", got.Detail)
	}
}

func TestDetect_NotApplicable_NoIncus(t *testing.T) {
	d := fixedDetector(false, 0, false, false, "", nil, nil)
	got := d.Detect()
	if got.State != StateNotApplicable {
		t.Fatalf("state = %q, want %q", got.State, StateNotApplicable)
	}
}

func TestDetect_NotApplicable_NoGroup(t *testing.T) {
	d := fixedDetector(true, -1, false, false, "", nil, nil)
	got := d.Detect()
	if got.State != StateNotApplicable {
		t.Fatalf("state = %q, want %q", got.State, StateNotApplicable)
	}
}

func TestDetect_ProcessGroupsError_IsStale(t *testing.T) {
	// If we can't read our own groups but the user is a member, be conservative
	// and surface stale (prompt the user) rather than silently OK.
	d := fixedDetector(true, 994, true, true, "ubuntu", nil, errors.New("boom"))
	got := d.Detect()
	if got.State != StateStale {
		t.Fatalf("state = %q, want %q", got.State, StateStale)
	}
}

func TestProcessGroupsFromStatus(t *testing.T) {
	status := "Name:\tpalmux2\nUid:\t1000\t1000\t1000\t1000\nGid:\t1000\t1000\t1000\t1000\nGroups:\t4 24 27 994 1000 \n"
	groups, err := ProcessGroupsFromStatus(status)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Supplementary groups PLUS the effective primary gid (Gid: field 2 = 1000).
	want := map[int]bool{4: true, 24: true, 27: true, 994: true, 1000: true}
	got := map[int]bool{}
	for _, g := range groups {
		got[g] = true
	}
	for g := range want {
		if !got[g] {
			t.Fatalf("groups = %v, missing %d", groups, g)
		}
	}
}

// TestProcessGroupsFromStatus_PrimaryGidIncluded ensures a process whose PRIMARY
// gid is incus-admin (and which lacks it as a supplementary group) is detected
// as having the group — not misclassified as stale.
func TestProcessGroupsFromStatus_PrimaryGidIncluded(t *testing.T) {
	status := "Name:\tx\nGid:\t994\t994\t994\t994\nGroups:\t4 1000 \n"
	groups, err := ProcessGroupsFromStatus(status)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, g := range groups {
		if g == 994 {
			found = true
		}
	}
	if !found {
		t.Fatalf("primary gid 994 not included in %v", groups)
	}
}

func TestProcessGroupsFromStatus_NoGroupsLine(t *testing.T) {
	if _, err := ProcessGroupsFromStatus("Name:\tx\nUid:\t0\n"); err == nil {
		t.Fatal("expected error when no Groups: line present")
	}
}

// TestDetect_RealProcess_DoesNotPanic exercises the default detector against the
// real running test process. It must classify into one of the known states
// without error (the actual state depends on the test host).
func TestDetect_RealProcess(t *testing.T) {
	d := NewDefaultDetector()
	got := d.Detect()
	switch got.State {
	case StateOK, StateStale, StateNotMember, StateNotApplicable:
		// any is acceptable; we only assert it classifies + returns a remedy.
	default:
		t.Fatalf("unexpected state %q", got.State)
	}
	if got.Remedy == "" {
		t.Error("remedy must never be empty")
	}
}
