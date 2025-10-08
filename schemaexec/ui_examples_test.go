package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestUIExample_UserInput tests the UserInput transformation from SymbolicTab.tsx
// Query: {userId: .id, displayName: .name, tier: (if .score >= 90 then "gold" else "silver" end), location: {city: .address.city, zip: .address.postalCode}}
func TestUIExample_UserInput(t *testing.T) {
	query, err := gojq.Parse(`{userId: .id, displayName: .name, tier: (if .score >= 90 then "gold" else "silver" end), location: {city: .address.city, zip: .address.postalCode}}`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Input schema from OAS
	addressSchema := BuildObject(map[string]*oas3.Schema{
		"city":       StringType(),
		"postalCode": StringType(),
	}, []string{"city", "postalCode"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"id":      NumberType(), // integer in JSON becomes number in schema
		"name":    StringType(),
		"score":   NumberType(),
		"address": addressSchema,
	}, []string{"id", "name", "score", "address"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	// Validate output is object
	if getType(result.Schema) != "object" {
		t.Errorf("Expected object, got: %s", getType(result.Schema))
	}

	// Validate userId property
	if userId, ok := result.Schema.Properties.Get("userId"); ok && userId.Left != nil {
		userIdType := getType(userId.Left)
		t.Logf("✅ userId exists with type: %s", userIdType)
		if userIdType != "number" {
			t.Errorf("Expected userId type 'number', got '%s'", userIdType)
		}
	} else {
		t.Error("❌ Missing userId property")
	}

	// Validate displayName property
	if displayName, ok := result.Schema.Properties.Get("displayName"); ok && displayName.Left != nil {
		displayNameType := getType(displayName.Left)
		t.Logf("✅ displayName exists with type: %s", displayNameType)
		if displayNameType != "string" {
			t.Errorf("Expected displayName type 'string', got '%s'", displayNameType)
		}
	} else {
		t.Error("❌ Missing displayName property")
	}

	// Validate tier property (conditional result)
	if tier, ok := result.Schema.Properties.Get("tier"); ok && tier.Left != nil {
		tierType := getType(tier.Left)
		t.Logf("✅ tier exists with type: %s", tierType)
		if tierType != "string" {
			t.Errorf("Expected tier type 'string', got '%s'", tierType)
		}
		// Check if enum values are "gold" and "silver" (conservative union)
		if tier.Left.Enum != nil && len(tier.Left.Enum) > 0 {
			t.Logf("  tier enum values: %d values", len(tier.Left.Enum))
		}
	} else {
		t.Error("❌ Missing tier property")
	}

	// Validate location property (nested object)
	if location, ok := result.Schema.Properties.Get("location"); ok && location.Left != nil {
		locationType := getType(location.Left)
		t.Logf("✅ location exists with type: %s", locationType)
		if locationType != "object" {
			t.Errorf("Expected location type 'object', got '%s'", locationType)
		}

		// Validate location.city
		if location.Left.Properties != nil {
			if city, ok := location.Left.Properties.Get("city"); ok && city.Left != nil {
				cityType := getType(city.Left)
				t.Logf("  ✅ location.city exists with type: %s", cityType)
				if cityType != "string" {
					t.Errorf("Expected city type 'string', got '%s'", cityType)
				}
			} else {
				t.Error("  ❌ Missing location.city property")
			}

			// Validate location.zip (remapped from postalCode)
			if zip, ok := location.Left.Properties.Get("zip"); ok && zip.Left != nil {
				zipType := getType(zip.Left)
				t.Logf("  ✅ location.zip exists with type: %s", zipType)
				if zipType != "string" {
					t.Errorf("Expected zip type 'string', got '%s'", zipType)
				}
			} else {
				t.Error("  ❌ Missing location.zip property")
			}
		}
	} else {
		t.Error("❌ Missing location property")
	}

	t.Logf("Warnings: %v", result.Warnings)
	t.Log("✅ UserInput transformation complete!")
}

// TestUIExample_ProductInput tests the ProductInput transformation from SymbolicTab.tsx
// Query: {productId: .id, displayName: .name, total: (.price * .quantity), tags: (.tags | map({value: .}))}
func TestUIExample_ProductInput(t *testing.T) {
	query, err := gojq.Parse(`{productId: .id, displayName: .name, total: (.price * .quantity), tags: (.tags | map({value: .}))}`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Input schema from OAS
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"id":       StringType(),
		"name":     StringType(),
		"price":    NumberType(),
		"quantity": NumberType(), // integer in OAS
		"tags":     ArrayType(StringType()),
	}, []string{"id", "name", "price", "quantity", "tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	// Validate output is object
	if getType(result.Schema) != "object" {
		t.Errorf("Expected object, got: %s", getType(result.Schema))
	}

	// Validate productId property
	if productId, ok := result.Schema.Properties.Get("productId"); ok && productId.Left != nil {
		productIdType := getType(productId.Left)
		t.Logf("✅ productId exists with type: %s", productIdType)
		if productIdType != "string" {
			t.Errorf("Expected productId type 'string', got '%s'", productIdType)
		}
	} else {
		t.Error("❌ Missing productId property")
	}

	// Validate displayName property
	if displayName, ok := result.Schema.Properties.Get("displayName"); ok && displayName.Left != nil {
		displayNameType := getType(displayName.Left)
		t.Logf("✅ displayName exists with type: %s", displayNameType)
		if displayNameType != "string" {
			t.Errorf("Expected displayName type 'string', got '%s'", displayNameType)
		}
	} else {
		t.Error("❌ Missing displayName property")
	}

	// Validate total property (arithmetic)
	if total, ok := result.Schema.Properties.Get("total"); ok && total.Left != nil {
		totalType := getType(total.Left)
		t.Logf("✅ total exists with type: %s", totalType)
		if totalType != "number" {
			t.Errorf("Expected total type 'number', got '%s'", totalType)
		}
	} else {
		t.Error("❌ Missing total property")
	}

	// Validate tags property (map transformation)
	if tags, ok := result.Schema.Properties.Get("tags"); ok && tags.Left != nil {
		tagsType := getType(tags.Left)
		t.Logf("✅ tags exists with type: %s", tagsType)
		if tagsType != "array" {
			t.Errorf("Expected tags type 'array', got '%s'", tagsType)
		}

		// Validate array items are objects with 'value' property
		if tags.Left.Items != nil && tags.Left.Items.Left != nil {
			itemType := getType(tags.Left.Items.Left)
			t.Logf("  tags items type: %s", itemType)
			if itemType != "object" {
				t.Errorf("Expected tags items type 'object', got '%s'", itemType)
			}

			// Validate value property in items
			if tags.Left.Items.Left.Properties != nil {
				if value, ok := tags.Left.Items.Left.Properties.Get("value"); ok && value.Left != nil {
					valueType := getType(value.Left)
					t.Logf("  ✅ tags items have 'value' property with type: %s", valueType)
					if valueType != "string" {
						t.Errorf("Expected value type 'string', got '%s'", valueType)
					}
				} else {
					t.Error("  ❌ Missing 'value' property in tags items")
				}
			}
		} else {
			t.Error("❌ tags array has no items schema")
		}
	} else {
		t.Error("❌ Missing tags property")
	}

	t.Logf("Warnings: %v", result.Warnings)
	t.Log("✅ ProductInput transformation complete!")
}

// TestUIExample_CartInput tests the CartInput transformation from SymbolicTab.tsx
// Query: {grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}
func TestUIExample_CartInput(t *testing.T) {
	query, err := gojq.Parse(`{grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Input schema from OAS
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"sku":      StringType(),
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"sku", "price", "quantity"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(itemSchema),
	}, []string{"items"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	// Validate output is object
	if getType(result.Schema) != "object" {
		t.Errorf("Expected object, got: %s", getType(result.Schema))
	}

	// Validate grandTotal property (aggregated arithmetic)
	if grandTotal, ok := result.Schema.Properties.Get("grandTotal"); ok && grandTotal.Left != nil {
		grandTotalType := getType(grandTotal.Left)
		t.Logf("✅ grandTotal exists with type: %s", grandTotalType)
		if grandTotalType != "number" {
			t.Errorf("Expected grandTotal type 'number', got '%s'", grandTotalType)
		}
	} else {
		t.Error("❌ Missing grandTotal property")
	}

	// Validate items property (nested map with shorthand)
	if items, ok := result.Schema.Properties.Get("items"); ok && items.Left != nil {
		itemsType := getType(items.Left)
		t.Logf("✅ items exists with type: %s", itemsType)
		if itemsType != "array" {
			t.Errorf("Expected items type 'array', got '%s'", itemsType)
		}

		// Validate array items are objects with 'sku' and 'total'
		if items.Left.Items != nil && items.Left.Items.Left != nil {
			itemType := getType(items.Left.Items.Left)
			t.Logf("  items elements type: %s", itemType)
			if itemType != "object" {
				t.Errorf("Expected items elements type 'object', got '%s'", itemType)
			}

			// Validate sku property (copied via shorthand)
			if items.Left.Items.Left.Properties != nil {
				if sku, ok := items.Left.Items.Left.Properties.Get("sku"); ok && sku.Left != nil {
					skuType := getType(sku.Left)
					t.Logf("  ✅ items elements have 'sku' property with type: %s", skuType)
					if skuType != "string" {
						t.Errorf("Expected sku type 'string', got '%s'", skuType)
					}
				} else {
					t.Error("  ❌ Missing 'sku' property in items elements")
				}

				// Validate total property (computed)
				if total, ok := items.Left.Items.Left.Properties.Get("total"); ok && total.Left != nil {
					totalType := getType(total.Left)
					t.Logf("  ✅ items elements have 'total' property with type: %s", totalType)
					if totalType != "number" {
						t.Errorf("Expected total type 'number', got '%s'", totalType)
					}
				} else {
					t.Error("  ❌ Missing 'total' property in items elements")
				}
			}
		} else {
			t.Error("❌ items array has no items schema")
		}
	} else {
		t.Error("❌ Missing items property")
	}

	t.Logf("Warnings: %v", result.Warnings)
	t.Log("✅ CartInput transformation complete!")
}
