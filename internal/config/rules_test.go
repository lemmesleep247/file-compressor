package config

import "testing"

func TestSettingsForAppliesBucketOverrides(t *testing.T) {
	ImageQuality, MaxImageDimension, EnableWebP = 75, 0, false
	rules, err := parseRules(`{"photos":{"image_quality":60,"max_dimension":2048,"webp":true},"docs":{"image_quality":90}}`)
	if err != nil {
		t.Fatal(err)
	}
	BucketRules = rules
	defer func() { BucketRules = nil }()

	if got := SettingsFor("photos"); got != (Settings{60, 2048, true}) {
		t.Errorf("photos: %+v", got)
	}
	if got := SettingsFor("docs"); got != (Settings{90, 0, false}) {
		t.Errorf("docs inherits unset fields: %+v", got)
	}
	if got := SettingsFor("other"); got != (Settings{75, 0, false}) {
		t.Errorf("unlisted bucket uses globals: %+v", got)
	}
}

func TestParseRulesRejectsBadInput(t *testing.T) {
	for _, bad := range []string{`{`, `{"b":{"quality":50}}`, `[1]`} {
		if _, err := parseRules(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if m, err := parseRules("  "); err != nil || m != nil {
		t.Errorf("empty: %v %v", m, err)
	}
}

func TestValidateRanges(t *testing.T) {
	defer func() { BucketRules, rulesErr, ImageQuality, MaxImageDimension = nil, nil, 75, 0 }()

	ImageQuality = 75
	q := 0
	BucketRules = map[string]BucketRule{"b": {ImageQuality: &q}}
	if Validate() == nil {
		t.Error("quality 0 accepted")
	}
	BucketRules = nil
	ImageQuality = 101
	if Validate() == nil {
		t.Error("quality 101 accepted")
	}
	ImageQuality = 75
	if err := Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
