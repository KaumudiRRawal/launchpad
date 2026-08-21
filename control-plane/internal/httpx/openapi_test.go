package httpx

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/analyze"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/openapi"
)

// The dashboard's client is generated from the OpenAPI document, which makes
// the document load-bearing rather than documentation: a route or a field it
// gets wrong becomes a client that compiles and then fails against the real
// API. These tests hold it to the two things it describes — the routes the
// router serves, and the structs the handlers encode.

// specification is the slice of the document these tests read. It is
// deliberately partial; the assertions are about the API's shape, not about
// modelling all of OpenAPI in Go.
type specification struct {
	OpenAPI string                          `yaml:"openapi"`
	Paths   map[string]map[string]operation `yaml:"paths"`

	Components struct {
		Schemas map[string]schema `yaml:"schemas"`
	} `yaml:"components"`
}

type operation struct {
	OperationID string `yaml:"operationId"`
	// A pointer distinguishes an absent `security` key, which inherits the
	// document's global bearer-token requirement, from `security: []`, which
	// is how an operation declares itself public.
	Security *[]map[string][]string `yaml:"security"`
}

func (o operation) public() bool { return o.Security != nil && len(*o.Security) == 0 }

type schema struct {
	Required   []string          `yaml:"required"`
	Properties map[string]schema `yaml:"properties"`
}

func loadSpecification(t *testing.T) specification {
	t.Helper()

	var spec specification
	if err := yaml.Unmarshal(openapi.Document, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi = %q, want a 3.x document", spec.OpenAPI)
	}
	return spec
}

func TestSpecificationDescribesEveryRoute(t *testing.T) {
	spec := loadSpecification(t)

	// Keyed the same way a route is, so the two lists can be compared by
	// subtraction and whatever is left over is a genuine disagreement.
	described := make(map[string]operation, len(spec.Paths))
	for path, item := range spec.Paths {
		for method, op := range item {
			described[strings.ToUpper(method)+" "+path] = op
		}
	}

	for _, rt := range (&API{}).routes() {
		endpoint := rt.method + " " + rt.path

		op, ok := described[endpoint]
		if !ok {
			t.Errorf("the router serves %s, which the specification does not describe", endpoint)
			continue
		}
		delete(described, endpoint)

		// The generated client names its methods after the operation ID, so an
		// operation without one produces an unreadable name derived from the
		// path instead.
		if op.OperationID == "" {
			t.Errorf("%s has no operationId", endpoint)
		}
		if op.public() != rt.public {
			t.Errorf("%s is %s in the router but %s in the specification",
				endpoint, authWord(rt.public), authWord(op.public()))
		}
	}

	for _, endpoint := range slices.Sorted(maps.Keys(described)) {
		t.Errorf("the specification describes %s, which the router does not serve", endpoint)
	}
}

func authWord(public bool) string {
	if public {
		return "public"
	}
	return "authenticated"
}

func TestSchemasMatchTheEncodedTypes(t *testing.T) {
	spec := loadSpecification(t)

	tests := []struct {
		schema string
		value  any
		// checkRequired holds a schema's `required` list to the fields the
		// encoder always emits. Only response bodies are checked: a request
		// field the server defaults is optional to send but always present in
		// the reply, so the two lists are legitimately different there.
		checkRequired bool
	}{
		{schema: "Project", value: domain.Project{}, checkRequired: true},
		{schema: "Service", value: domain.Service{}, checkRequired: true},
		{schema: "Environment", value: domain.Environment{}, checkRequired: true},
		{schema: "Deployment", value: domain.Deployment{}, checkRequired: true},
		{schema: "DeploymentLog", value: domain.DeploymentLog{}, checkRequired: true},
		{schema: "MetricSummary", value: domain.MetricSummary{}, checkRequired: true},
		{schema: "MetricWindow", value: domain.MetricWindow{}, checkRequired: true},
		{schema: "MetricPoint", value: domain.MetricPoint{}, checkRequired: true},
		{schema: "EnvironmentMetrics", value: domain.EnvironmentMetrics{}, checkRequired: true},
		{schema: "AnalysisReport", value: analyze.Report{}, checkRequired: true},
		{schema: "Finding", value: analyze.Finding{}, checkRequired: true},
		{schema: "Remediation", value: analyze.Remediation{}, checkRequired: true},
		{schema: "APIKey", value: domain.APIKey{}, checkRequired: true},

		{schema: "CreateProjectInput", value: domain.CreateProjectInput{}},
		{schema: "CreateServiceInput", value: domain.CreateServiceInput{}},
		{schema: "CreateEnvironmentInput", value: domain.CreateEnvironmentInput{}},
		{schema: "CreateDeploymentInput", value: domain.CreateDeploymentInput{}},
		{schema: "CreateAPIKeyInput", value: domain.CreateAPIKeyInput{}},
	}

	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			declared, ok := spec.Components.Schemas[tt.schema]
			if !ok {
				t.Fatalf("the specification has no %s schema", tt.schema)
			}

			encoded, always := jsonFields(reflect.TypeOf(tt.value))
			compare(t, tt.schema+" properties", slices.Sorted(maps.Keys(declared.Properties)), encoded)

			if tt.checkRequired {
				required := slices.Clone(declared.Required)
				slices.Sort(required)
				compare(t, tt.schema+" required", required, always)
			}
		})
	}
}

func TestErrorSchemaMatchesTheResponseEnvelope(t *testing.T) {
	spec := loadSpecification(t)

	// Every failure in the API is this one shape, so a client written against
	// the document parses any error it is ever handed.
	declared, ok := spec.Components.Schemas["Error"].Properties["error"]
	if !ok {
		t.Fatal("the Error schema has no error property")
	}

	var body ErrorBody
	encoded, _ := jsonFields(reflect.TypeOf(body.Error))
	compare(t, "Error.error properties", slices.Sorted(maps.Keys(declared.Properties)), encoded)
}

func TestOpenAPIEndpointServesTheEmbeddedDocument(t *testing.T) {
	// No store is wired, so this also proves the route is reachable without a
	// credential: authentication would panic on the nil Authenticator.
	srv := newTestAPI(nil).Routes()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), openapi.Document) {
		t.Error("the served body is not the embedded document")
	}
}

// jsonFields reports the JSON names a struct encodes to: every field, and the
// subset the encoder always emits. A field tagged omitempty is left out of the
// second list, which is exactly what makes it optional in the schema.
func jsonFields(t reflect.Type) (all, always []string) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}

		all = append(all, name)
		if !slices.Contains(strings.Split(options, ","), "omitempty") {
			always = append(always, name)
		}
	}

	slices.Sort(all)
	slices.Sort(always)
	return all, always
}

// compare reports each name that only one of the two lists has, so a
// disagreement names the field rather than printing two lists to diff by eye.
func compare(t *testing.T, label string, declared, encoded []string) {
	t.Helper()

	for _, name := range missing(declared, encoded) {
		t.Errorf("%s: the specification declares %q, which the Go type does not encode", label, name)
	}
	for _, name := range missing(encoded, declared) {
		t.Errorf("%s: the Go type encodes %q, which the specification does not declare", label, name)
	}
}

// missing returns the entries of a that b does not contain.
func missing(a, b []string) []string {
	var out []string
	for _, name := range a {
		if !slices.Contains(b, name) {
			out = append(out, name)
		}
	}
	return out
}
