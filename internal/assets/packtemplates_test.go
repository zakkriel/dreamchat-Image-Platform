package assets

import (
	"reflect"
	"testing"
)

// Phase 5B pack templates: each named template resolves to its documented
// role set under the correct entity type, and is unknown for the other.

func TestPackTemplateRolesCharacter(t *testing.T) {
	cases := map[string][]string{
		TemplateCharacterMinimalPortrait: {
			"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait",
			"warm_or_smiling_expression", "serious_or_tense_expression",
			"angry_or_defensive_expression", "surprised_or_shocked_expression",
		},
		TemplateCharacterExpression: {
			"neutral_front_portrait", "expression_warm", "expression_serious",
			"expression_angry", "expression_surprised",
		},
		TemplateCharacterFullReference: {
			"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait",
			"expression_warm", "expression_serious", "expression_angry", "expression_surprised",
			"full_body_reference", "profile_icon",
		},
	}
	for tmpl, want := range cases {
		got, ok := PackTemplateRoles(EntityCharacter, tmpl)
		if !ok {
			t.Fatalf("%s should be a known character template", tmpl)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s roles: expected %v, got %v", tmpl, want, got)
		}
		// Every documented role classifies (no unknowns in a template).
		for _, role := range got {
			if c := ClassifyVariant(EntityCharacter, role); c.Family == FamilyUnknown {
				t.Fatalf("template %s role %q classifies as unknown", tmpl, role)
			}
		}
	}
}

func TestPackTemplateRolesPlace(t *testing.T) {
	cases := map[string][]string{
		TemplatePlaceMinimalScene: {
			"establishing_wide_view", "closer_atmospheric_view",
			"day_view", "night_view", "calm_or_empty_view", "busy_or_active_view",
		},
		TemplatePlaceTimeOfDay: {"day_view", "night_view", "dawn_view", "dusk_view"},
		TemplatePlaceState:     {"state_damaged", "state_rebuilt", "state_occupied"},
	}
	for tmpl, want := range cases {
		got, ok := PackTemplateRoles(EntityPlace, tmpl)
		if !ok {
			t.Fatalf("%s should be a known place template", tmpl)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s roles: expected %v, got %v", tmpl, want, got)
		}
		for _, role := range got {
			if c := ClassifyVariant(EntityPlace, role); c.Family == FamilyUnknown {
				t.Fatalf("template %s role %q classifies as unknown", tmpl, role)
			}
		}
	}
}

func TestPackTemplateRolesUnknownAndCrossEntity(t *testing.T) {
	if _, ok := PackTemplateRoles(EntityCharacter, "no_such_template"); ok {
		t.Fatalf("unknown template must report ok=false")
	}
	// A place template is not a character template and vice versa.
	if _, ok := PackTemplateRoles(EntityCharacter, TemplatePlaceTimeOfDay); ok {
		t.Fatalf("place template must be unknown under character entity")
	}
	if _, ok := PackTemplateRoles(EntityPlace, TemplateCharacterExpression); ok {
		t.Fatalf("character template must be unknown under place entity")
	}
	if _, ok := PackTemplateRoles("artifact", TemplateCharacterExpression); ok {
		t.Fatalf("unknown entity must report ok=false")
	}
}

func TestPackTemplateRolesReturnsCopy(t *testing.T) {
	got, _ := PackTemplateRoles(EntityCharacter, TemplateCharacterMinimalPortrait)
	got[0] = "mutated"
	again, _ := PackTemplateRoles(EntityCharacter, TemplateCharacterMinimalPortrait)
	if again[0] != "neutral_front_portrait" {
		t.Fatalf("template table leaked mutation: %v", again)
	}
}

func TestPackTemplatesRespectVariantCap(t *testing.T) {
	// All templates must fit within the fan-out cap so a template request can
	// never be rejected for size.
	const cap = 12
	for _, ent := range []string{EntityCharacter, EntityPlace} {
		var table map[string][]string
		if ent == EntityCharacter {
			table = characterPackTemplates
		} else {
			table = placePackTemplates
		}
		for tmpl, roles := range table {
			if len(roles) > cap {
				t.Fatalf("template %s has %d roles, exceeds cap %d", tmpl, len(roles), cap)
			}
		}
	}
}
