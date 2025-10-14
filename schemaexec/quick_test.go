package schemaexec
import ("testing"; "context"; gojq "github.com/speakeasy-api/jq")
func TestQuickFieldSelect(t *testing.T) {
	query, _ := gojq.Parse(`{offset: 42, string: "foo"} | .string`)
	result, err := RunSchema(context.Background(), query, ObjectType())
	if err != nil { t.Fatal(err) }
	t.Logf("Result type: %s", getType(result.Schema))
	if getType(result.Schema) != "string" {
		t.Errorf("Expected string, got %s", getType(result.Schema))
	}
}
