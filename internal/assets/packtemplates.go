package assets

// Pack templates (Phase 5B, PRD 04 §8). A named template resolves to a fixed,
// ordered set of variant roles. The generate-pack handlers use this to turn a
// `pack_template` request field into the variant list to fan out, and to name
// the resulting asset_pack's pack_type.
//
// Resolution precedence (enforced by the handler):
//   explicit variant_keys > pack_template > minimal default
//
// When explicit variant_keys override a template, the pack is NOT labelled as
// the named template — it is a custom pack (character_custom_pack /
// place_custom_pack), because its contents are caller-defined.
//
// 5B defines the role *sets* in code but deliberately does NOT persist
// pack-completeness (delivered-vs-missing required roles): asset_packs has no
// column for it, and adding one is out of scope (deferred to 6A / a schema
// phase). See IMPLEMENTATION_STATUS.md.

// Character pack template names (PRD 04 §8.1).
const (
	TemplateCharacterMinimalPortrait = "character_minimal_portrait_pack"
	TemplateCharacterExpression      = "character_expression_pack"
	TemplateCharacterFullReference   = "character_full_reference_pack"

	// Custom pack type when explicit variant_keys override a template.
	PackTypeCharacterCustom = "character_custom_pack"
)

// Place pack template names (PRD 04 §8.2).
const (
	TemplatePlaceMinimalScene = "place_minimal_scene_pack"
	TemplatePlaceTimeOfDay    = "place_time_of_day_pack"
	TemplatePlaceState        = "place_state_pack"

	// Custom pack type when explicit variant_keys override a template.
	PackTypePlaceCustom = "place_custom_pack"
)

// characterPackTemplates maps each character template to its ordered roles.
var characterPackTemplates = map[string][]string{
	TemplateCharacterMinimalPortrait: {
		"neutral_front_portrait",
		"neutral_three_quarter_portrait",
		"side_angle_portrait",
	},
	TemplateCharacterExpression: {
		"neutral_front_portrait",
		"expression_warm",
		"expression_serious",
		"expression_angry",
		"expression_surprised",
	},
	TemplateCharacterFullReference: {
		"neutral_front_portrait",
		"neutral_three_quarter_portrait",
		"side_angle_portrait",
		"expression_warm",
		"expression_serious",
		"expression_angry",
		"expression_surprised",
		"full_body_reference",
		"profile_icon",
	},
}

// placePackTemplates maps each place template to its ordered roles.
var placePackTemplates = map[string][]string{
	TemplatePlaceMinimalScene: {
		"establishing_wide_view",
		"closer_atmospheric_view",
	},
	TemplatePlaceTimeOfDay: {
		"day_view",
		"night_view",
		"dawn_view",
		"dusk_view",
	},
	TemplatePlaceState: {
		"state_damaged",
		"state_rebuilt",
		"state_occupied",
	},
}

// PackTemplateRoles returns the ordered role set for a named template under
// the given entity type, and whether the template is known for that entity.
// A template defined for the other entity type returns (nil, false).
func PackTemplateRoles(entityType, template string) ([]string, bool) {
	var table map[string][]string
	switch entityType {
	case EntityCharacter:
		table = characterPackTemplates
	case EntityPlace:
		table = placePackTemplates
	default:
		return nil, false
	}
	roles, ok := table[template]
	if !ok {
		return nil, false
	}
	return copyTagList(roles), true
}
