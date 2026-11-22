package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

func boolUseStateForUnknown() planmodifier.Bool { return boolplanmodifier.UseStateForUnknown() }
