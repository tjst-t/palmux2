package parser

import "testing"

func TestParseVerifyRun(t *testing.T) {
	src := []byte(`{
		"sprint":"Sx","command_source":"declared (.claude/verify.json)",
		"runs":[
			{"name":"unit","command":"go test","exit_code":0,"log":"docs/sprint-logs/Sx/verify-run-unit.log","machine_status":"pass","junit":{"total":5,"passed":5}},
			{"name":"e2e","exit_code":1,"machine_status":"fail"}
		],
		"overall_machine_status":"fail"
	}`)
	vr := ParseVerifyRun(src)
	if vr == nil {
		t.Fatal("nil")
	}
	if vr.OverallMachineStatus != "fail" {
		t.Errorf("overall: %q", vr.OverallMachineStatus)
	}
	if len(vr.Runs) != 2 {
		t.Fatalf("runs: %d", len(vr.Runs))
	}
	if vr.Runs[0].Junit == nil || vr.Runs[0].Junit.Passed != 5 {
		t.Errorf("junit: %+v", vr.Runs[0].Junit)
	}
	if vr.Runs[0].Log == "" {
		t.Errorf("log path lost")
	}
	if ParseVerifyRun(nil) != nil {
		t.Errorf("nil input should yield nil")
	}
	if ParseVerifyRun([]byte("{bad")) != nil {
		t.Errorf("corrupt input should yield nil (fail-open)")
	}
}

func TestParseVerificationReport(t *testing.T) {
	src := []byte(`{
		"sprint":"Sx","overall":"warn",
		"stories":{
			"Sx-1":{"verdict":"ok","ac_findings":[
				{"ac":"AC-Sx-1-1","status":"pass","evidence":"file:1"},
				{"ac":"AC-Sx-1-2","status":"warn","evidence":"gap","overlooked_by_autopilot":true,"recommended_action":"add test"}
			],"forbidden_category_findings":[]}
		},
		"findings":[{"category":"test_coverage","story":"Sx-1","ac":"AC-Sx-1-2","verdict":"warn","overlooked_by_autopilot":true}],
		"summary":{"ac_failures":0,"ac_warnings":1,"overlooked_count":1}
	}`)
	vr := ParseVerificationReport(src)
	if vr == nil {
		t.Fatal("nil")
	}
	if vr.Overall != "warn" || vr.Summary.OverlookedCount != 1 {
		t.Errorf("overall/summary: %q %+v", vr.Overall, vr.Summary)
	}
	if len(vr.Stories) != 1 || len(vr.Stories[0].ACFindings) != 2 {
		t.Fatalf("stories: %+v", vr.Stories)
	}
	if !vr.Stories[0].ACFindings[1].OverlookedByAutopilot {
		t.Errorf("overlooked flag lost")
	}
	if len(vr.Findings) != 1 {
		t.Errorf("findings: %d", len(vr.Findings))
	}
}

func TestParseDoneJudgment(t *testing.T) {
	src := []byte(`{
		"sprint":"Sx",
		"precondition":{"detail_level":"detailed","stories_nonempty":true,"ok":true},
		"stories":{
			"Sx-1":{
				"guard_1_not_user_review_required":"pass — ok",
				"guard_2_nil_injection_mock":"pass",
				"guard_7_adr_conformance":"n/a — no ADRs",
				"overall":"ok",
				"note":"fine"
			}
		}
	}`)
	dj := ParseDoneJudgment(src)
	if dj == nil {
		t.Fatal("nil")
	}
	if dj.Precondition == nil || !dj.Precondition.OK {
		t.Errorf("precondition: %+v", dj.Precondition)
	}
	if len(dj.Stories) != 1 {
		t.Fatalf("stories: %d", len(dj.Stories))
	}
	s := dj.Stories[0]
	if s.Overall != "ok" || s.Note != "fine" {
		t.Errorf("overall/note: %q %q", s.Overall, s.Note)
	}
	if len(s.Guards) != 3 {
		t.Fatalf("guards: %d", len(s.Guards))
	}
	// guards sorted by num: 1,2,7
	if s.Guards[0].Num != 1 || s.Guards[0].Status != "pass" {
		t.Errorf("guard0: %+v", s.Guards[0])
	}
	if s.Guards[2].Num != 7 || s.Guards[2].Status != "n/a" {
		t.Errorf("guard2: %+v", s.Guards[2])
	}
	if s.Guards[0].Label == "" {
		t.Errorf("label empty")
	}
}

func TestParseCompromises_TopLevelAndNested(t *testing.T) {
	top := []byte(`{"stopped_at":"m","compromises":[{"type":"test_assertion_weakened","severity":"high","rationale":"x"}],"blockers_encountered":[{"type":"b","severity":"medium","detail":"d"}],"scope_changes":[]}`)
	c := ParseCompromises(top)
	if c == nil || len(c.Compromises) != 1 || c.Compromises[0].Severity != "high" {
		t.Fatalf("top-level: %+v", c)
	}
	if len(c.Blockers) != 1 || c.Blockers[0].Detail != "d" {
		t.Errorf("blockers: %+v", c.Blockers)
	}
	nested := []byte(`{"milestone_summary":{"compromises":[{"type":"error_swallowed","severity":"low"}],"blockers_encountered":[],"scope_changes":[]}}`)
	c2 := ParseCompromises(nested)
	if c2 == nil || len(c2.Compromises) != 1 || c2.Compromises[0].Type != "error_swallowed" {
		t.Fatalf("nested: %+v", c2)
	}
}

func TestParseReopenAndComprehension(t *testing.T) {
	rp := ParseReopen([]byte(`{"sprint_id":"Sx","reopened_at":"2026-01-01T00:00:00Z","triggered_by":"milestone_review","reason":"AC violation","affected_acceptance_criteria":["AC-Sx-1-1"],"added_tasks":[{"story":"Sx-1","task_id":"Sx-1-fix-1","description":"fix"}]}`))
	if rp == nil || rp.TriggeredBy != "milestone_review" || len(rp.AddedTasks) != 1 {
		t.Fatalf("reopen: %+v", rp)
	}
	c := ParseComprehension([]byte("# Title\n\n## What changed\n\n- a\n\n### Why this way\n\ntext"))
	if c == nil || c.Markdown == "" {
		t.Fatal("comprehension nil")
	}
	found := false
	for _, h := range c.Headings {
		if h == "What changed" {
			found = true
		}
	}
	if !found {
		t.Errorf("headings missing 'What changed': %v", c.Headings)
	}
}

func TestClassifySmokeLog(t *testing.T) {
	if got := ClassifySmokeLog("deploy-test-smoke.json", []byte(`{"kind":"real-incus","overall":"PASS"}`)); got.Overall != "pass" || got.Kind != "real-incus" {
		t.Errorf("pass: %+v", got)
	}
	if got := ClassifySmokeLog("x.json", []byte(`{"status":"FAILED"}`)); got.Overall != "fail" {
		t.Errorf("fail: %+v", got)
	}
	if got := ClassifySmokeLog("x.json", []byte(`{"note":"n/a"}`)); got.Overall != "unknown" {
		t.Errorf("unknown: %+v", got)
	}
	if got := ClassifySmokeLog("x.json", []byte("{bad")); got.Overall != "unknown" {
		t.Errorf("corrupt should be unknown: %+v", got)
	}
}

func TestParsePrototypeReview(t *testing.T) {
	pr := ParsePrototypeReview([]byte(`{"sprint_range":["Sx"],"screens":[{"file":"prototype/a.html","story":"Sx-1","feedback_rounds":1,"approved":true}],"design_decisions":["d1"],"approved_by_user":true,"approved_at":"2026-01-01"}`))
	if pr == nil || len(pr.Screens) != 1 || !pr.ApprovedByUser {
		t.Fatalf("proto: %+v", pr)
	}
	if pr.Screens[0].File != "prototype/a.html" {
		t.Errorf("screen: %+v", pr.Screens[0])
	}
}

func TestBuildACFindings_VerifierWins(t *testing.T) {
	sp := Sprint{
		ID: "Sx",
		Stories: []Story{{
			ID: "Sx-1",
			AcceptanceCriteria: []Acceptance{
				{ID: "AC-Sx-1-1", Description: "d1", Status: "pass"},
				{ID: "AC-Sx-1-2", Description: "d2", Status: "pass"},
			},
		}},
	}
	vr := &VerificationReport{Stories: []VerificationStory{{
		Story: "Sx-1",
		ACFindings: []VerificationAC{
			{AC: "AC-Sx-1-2", Status: "fail", Evidence: "no code", OverlookedByAutopilot: true},
		},
	}}}
	rows := BuildACFindings(sp, vr)
	if len(rows) != 2 {
		t.Fatalf("rows: %d", len(rows))
	}
	// AC-1-1: roadmap pass, no verifier finding → pass
	if rows[0].Status != "pass" {
		t.Errorf("row0: %+v", rows[0])
	}
	// AC-1-2: roadmap pass but verifier fail → fail (verifier wins)
	if rows[1].Status != "fail" || !rows[1].OverlookedByAutopilot {
		t.Errorf("row1 (verifier should win): %+v", rows[1])
	}
	// nil verifier → roadmap only, no crash
	rows2 := BuildACFindings(sp, nil)
	if len(rows2) != 2 || rows2[1].Status != "pass" {
		t.Errorf("nil-verifier rows: %+v", rows2)
	}
}

func TestRoadmap_NewFields(t *testing.T) {
	src := []byte(`{
		"project":"P","progress":{"total":2,"done":1},
		"execution_order":["Sx","Sy"],
		"sprints":{
			"Sx":{"title":"X","status":"in_progress","milestone":true,"detail_level":"detailed","phase":"Phase 6","review_reason":"pure gui",
				"stories":{
					"Sx-1":{"title":"S1","status":"needs_user_review","review_reason":"guard5","user_review_required":true,"depends_on":["Sx-2"],
						"acceptance_criteria":[{"id":"AC-Sx-1-1","description":"d","status":"pass","reopened_at":"2026-01-01"}]},
					"Sx-2":{"title":"S2","status":"blocked","needs_human":"creds","depends_on":"note string"}
				}},
			"Sy":{"title":"Y","status":"pending","detail_level":"coarse","stories":{}}
		},
		"dependencies":{"Sx":{"depends_on":["Sy"],"reason":"builds on Y"}},
		"backlog":[
			{"title":"GUI アプリカード (§8.3 install)","priority":"low","status":"pending","added_in":"Sd-plan"},
			{"title":"Unrelated widget refactor","status":"pending"}
		]
	}`)
	rm := ParseRoadmap(src)
	if len(rm.Sprints) != 2 {
		t.Fatalf("sprints: %d", len(rm.Sprints))
	}
	sx := rm.Sprints[0]
	if sx.DetailLevel != "detailed" || sx.Phase != "Phase 6" || sx.ReviewReason != "pure gui" || sx.Coarse {
		t.Errorf("sx meta: %+v", sx)
	}
	sy := rm.Sprints[1]
	if sy.DetailLevel != "coarse" || !sy.Coarse {
		t.Errorf("sy should be coarse: %+v", sy)
	}
	var s1, s2 *Story
	for i := range sx.Stories {
		switch sx.Stories[i].ID {
		case "Sx-1":
			s1 = &sx.Stories[i]
		case "Sx-2":
			s2 = &sx.Stories[i]
		}
	}
	if s1 == nil || s1.StatusKind != "needs-user-review" || !s1.UserReviewRequired || s1.ReviewReason != "guard5" {
		t.Errorf("s1: %+v", s1)
	}
	if len(s1.DependsOn) != 1 || s1.DependsOn[0] != "Sx-2" {
		t.Errorf("s1 depends_on array: %+v", s1.DependsOn)
	}
	if len(s1.AcceptanceCriteria) != 1 || s1.AcceptanceCriteria[0].ReopenedAt == "" {
		t.Errorf("ac reopened_at lost: %+v", s1.AcceptanceCriteria)
	}
	if s2 == nil || s2.StatusKind != "blocked" || s2.NeedsHuman != "creds" {
		t.Errorf("s2: %+v", s2)
	}
	if len(s2.DependsOn) != 1 || s2.DependsOn[0] != "note string" {
		t.Errorf("s2 depends_on string: %+v", s2.DependsOn)
	}
	// dependency reason
	var dep *Dependency
	for i := range rm.Dependencies {
		if rm.Dependencies[i].From == "Sx" {
			dep = &rm.Dependencies[i]
		}
	}
	if dep == nil || dep.Reason != "builds on Y" {
		t.Errorf("dep reason: %+v", dep)
	}
	// backlog fields + promotion. Item 0 shares "§8.3" ... but no sprint
	// title contains §8.3 here, so it should NOT be promoted. Verify fields.
	if len(rm.Backlog) != 2 {
		t.Fatalf("backlog: %d", len(rm.Backlog))
	}
	if rm.Backlog[0].Priority != "low" || rm.Backlog[0].Status != "pending" {
		t.Errorf("backlog fields: %+v", rm.Backlog[0])
	}
}

func TestDetectPromotion(t *testing.T) {
	sprints := []Sprint{
		{ID: "S41bdf2", Title: "§8.3 GUI アプリカードモデル"},
		{ID: "Sother", Title: "Unrelated things"},
	}
	if got := detectPromotion("GUI アプリカード (§8.3 install+share)", sprints); got != "S41bdf2" {
		t.Errorf("expected promotion to S41bdf2, got %q", got)
	}
	if got := detectPromotion("Commit-diff endpoint + Monaco diff", sprints); got != "" {
		t.Errorf("expected no promotion, got %q", got)
	}
}
