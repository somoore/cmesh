package profile

import "testing"

func TestGuideIncludesEveryBuiltinProfile(t *testing.T) {
	guide := Guide()
	if guide.DefaultRecommendation != Query || guide.FirstTool != "query_peer" {
		t.Fatalf("guide default = %q/%q, want query/query_peer", guide.DefaultRecommendation, guide.FirstTool)
	}
	seen := map[string]GuideItem{}
	for _, item := range guide.Profiles {
		seen[item.Name] = item
		if item.PlainLanguageContract == "" || item.Powers.Writes == "" || item.EnforcementStrength == "" {
			t.Fatalf("guide item %q is incomplete: %+v", item.Name, item)
		}
	}
	for _, want := range []string{Query, QueryNetwork, QueryShell, ReadonlyAgent, RepoDev, DesktopControl, SystemChange} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("guide missing %q in %+v", want, guide.Profiles)
		}
	}
	if seen[ReadonlyAgent].Powers.Writes != "denied" || seen[ReadonlyAgent].Powers.Deletes != "denied" {
		t.Fatalf("readonly-agent must explicitly deny writes/deletes: %+v", seen[ReadonlyAgent].Powers)
	}
}

func TestRecommendLeastPowerfulProfile(t *testing.T) {
	tests := []struct {
		intent string
		want   string
	}{
		{"check disk usage", Query},
		{"inspect repo status", Query},
		{"read files in the repo", ReadonlyAgent},
		{"query battery remaining", ReadonlyAgent},
		{"give me an app inventory sorted by install date", ReadonlyAgent},
		{"tell me the last time this system was rebooted", ReadonlyAgent},
		{"open /tmp/report.txt and tell me what was found", ReadonlyAgent},
		{"edit code and run tests", RepoDev},
		{"restart a service", SystemChange},
	}
	for _, tc := range tests {
		got := Recommend(tc.intent)
		if got.Profile != tc.want {
			t.Fatalf("Recommend(%q) = %q, want %q (%+v)", tc.intent, got.Profile, tc.want, got)
		}
		if got.Profile == RepoDev && (!got.RequiresConsent || !got.RequiresTTL || got.RecommendedTTL == "") {
			t.Fatalf("repo-dev recommendation must require consent+ttl: %+v", got)
		}
		if got.Profile == ReadonlyAgent && got.FirstTool != "inspect_peer" {
			t.Fatalf("readonly-agent recommendation should use inspect_peer: %+v", got)
		}
	}
}

func TestAcknowledgementProblemsForElevatedProfiles(t *testing.T) {
	problems := AcknowledgementProblems([]string{RepoDev}, nil)
	if len(problems) == 0 {
		t.Fatal("repo-dev without acknowledgement should require one")
	}
	ok := AcknowledgementProblems([]string{RepoDev}, []ProfileAcknowledgement{{
		Profile:         RepoDev,
		Writes:          WritesRepoOnly,
		Shell:           ShellAllow,
		Network:         false,
		TTL:             "30m",
		AllowedCWDScope: "~/code/myapp",
	}})
	if len(ok) != 0 {
		t.Fatalf("valid repo-dev acknowledgement problems = %+v", ok)
	}
	queryProblems := AcknowledgementProblems([]string{Query}, nil)
	if len(queryProblems) != 0 {
		t.Fatalf("query must not require acknowledgement: %+v", queryProblems)
	}
}
