package event

import (
	"errors"
	"testing"
	"time"
)

func ev(typ, scene string, actor ActorKind, refs map[string]string) Event {
	return Event{ID: typ + "-1", Source: "test", Type: typ, Scene: SceneRef{ID: scene, Kind: Group}, Actor: Actor{ID: "u1", Kind: actor}, OccurredAt: time.Unix(1, 0), Refs: refs}
}

func TestStandardCatalogCoversEveryClassAndMode(t *testing.T) {
	classes, modes := map[Class]bool{}, map[Mode]bool{}
	for _, s := range Standard().Types() {
		classes[s.Class], modes[s.Mode] = true, true
		if s.Mode == Listening && s.Lane == Foreground {
			t.Fatalf("%s: listening event in the foreground", s.Type)
		}
		for _, locale := range []string{"en", "zh-CN"} {
			if s.Summary[locale] == "" {
				t.Errorf("%s: no %s summary", s.Type, locale)
			}
		}
	}
	for _, c := range Classes {
		if !classes[c] {
			t.Errorf("no standard type of class %s", c)
		}
	}
	if !modes[Triggered] || !modes[Listening] {
		t.Errorf("both modes are represented: %v", modes)
	}
}

func TestValidateRejectsBrokenInvariants(t *testing.T) {
	summary := map[string]string{"en": "x"}
	bad := []TypeSpec{
		{Type: "x", Class: Business, Mode: Listening, Lane: Foreground, Session: Interactive, RequiresScene: true, Summary: summary},
		{Type: "x", Class: Completion, Mode: Triggered, Lane: Foreground, Session: Interactive, RequiresScene: true, Summary: summary},
		{Type: "x", Class: Execution, Mode: Triggered, Lane: Host, Session: FollowUp, RequiresScene: true, Summary: summary},
		{Type: "x", Class: Business, Mode: Listening, Lane: Background, Session: Observation, Summary: summary},
		{Type: "x", Class: Business, Mode: Listening, Lane: Background, Session: Observation, RequiresScene: true},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, s)
		}
	}
}

func TestAddressedMessagePreemptsObservation(t *testing.T) {
	adm, err := Standard().Route(ev("message.addressed", "s1", Human, nil))
	if err != nil {
		t.Fatal(err)
	}
	if adm.Session == nil || adm.Session.String() != "s1/interactive" || adm.Spec.Lane != Foreground {
		t.Fatalf("route %+v", adm)
	}
	if len(adm.Cancel) != 1 || adm.Cancel[0] != (Cancel{Scene: "s1", Kind: Observation, Preemptible: true, Reason: CancelPreempted}) {
		t.Fatalf("cancel %+v", adm.Cancel)
	}
	if adm.Event.Class != Business || adm.Event.Mode != Triggered {
		t.Fatalf("class and mode come from the catalog: %+v", adm.Event)
	}
}

func TestFollowUpSessionIsKeyedByTask(t *testing.T) {
	c := Standard()
	if _, err := c.Route(ev("execution.result", "s1", System, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a missing task ref is rejected, got %v", err)
	}
	adm, err := c.Route(ev("execution.result", "s1", System, map[string]string{RefTask: "t9"}))
	if err != nil || adm.Session.String() != "s1/follow_up/t9" {
		t.Fatalf("adm=%+v err=%v", adm, err)
	}
}

func TestRouteRejectsUnknownTypesAndMismatches(t *testing.T) {
	c := Standard()
	if _, err := c.Route(ev("nope", "s1", Human, nil)); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("unknown type: %v", err)
	}
	e := ev("message.observed", "s1", Human, nil)
	e.Mode = Triggered
	if _, err := c.Route(e); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mode mismatch: %v", err)
	}
	if _, err := c.Route(ev("message.addressed", "s1", System, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the foreground needs a human: %v", err)
	}
	if _, err := c.Route(ev("message.addressed", "", Human, nil)); !errors.Is(err, ErrMissingScene) {
		t.Fatalf("missing scene: %v", err)
	}
}

func TestEmployeeWideRevocation(t *testing.T) {
	adm, err := Standard().Route(ev("subscription.changed", "", System, nil))
	if err != nil {
		t.Fatal(err)
	}
	if adm.Session != nil || len(adm.Cancel) != 1 || adm.Cancel[0].Kind != Observation || adm.Cancel[0].Scene != "" {
		t.Fatalf("revokes observation in every scene: %+v", adm)
	}
}

func TestDescribeFallsBack(t *testing.T) {
	spec, _ := Standard().Lookup("routine.due")
	if spec.Describe("zh-Hans") != spec.Summary["zh-CN"] || spec.Describe("fr") != spec.Summary["en"] {
		t.Fatalf("fallback: %q %q", spec.Describe("zh-Hans"), spec.Describe("fr"))
	}
}

func TestFingerprintIgnoresDerivedFields(t *testing.T) {
	e := ev("message.addressed", "s1", Human, nil)
	f := e
	f.Class, f.Mode = Business, Triggered
	if e.Fingerprint() != f.Fingerprint() {
		t.Fatal("class and mode are derived")
	}
	f.Text = "changed"
	if e.Fingerprint() == f.Fingerprint() {
		t.Fatal("content changes the fingerprint")
	}
}
