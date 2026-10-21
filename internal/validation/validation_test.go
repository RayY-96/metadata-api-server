package validation

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

// multibyte returns n characters of 2 bytes each, so that a byte-based length
// check would fail where a character-based one passes.
func multibyte(n int) string { return strings.Repeat("é", n) }

const (
	emailDomain = "@example.com"
	urlPrefix   = "https://example.com/"
)

type maintainerTest struct {
	name      string
	in        model.MaintainerInput
	unchanged bool                  // the normalized copy equals in
	want      model.MaintainerInput // the normalized copy otherwise
	wantErrs  []FieldError          // the copy is not checked when set
}

func TestMaintainer(t *testing.T) {
	const name, mail = "Ada Lovelace", "ada@example.com"
	tests := []maintainerTest{
		{name: "valid", in: model.MaintainerInput{Name: name, Email: mail}, unchanged: true},
		{name: "fields are trimmed and email lower-cased",
			in:   model.MaintainerInput{Name: "  Ada  ", Email: " Ada@Example.COM\t"},
			want: model.MaintainerInput{Name: "Ada", Email: "ada@example.com"}},
		{name: "Unicode whitespace is trimmed",
			in:   model.MaintainerInput{Name: " Ada　", Email: " " + mail + "\n"},
			want: model.MaintainerInput{Name: "Ada", Email: mail}},
		{name: "non-ASCII email is lower-cased",
			in:   model.MaintainerInput{Name: name, Email: "JOSÉ@例子.中国"},
			want: model.MaintainerInput{Name: name, Email: "josé@例子.中国"}},
		{name: "domain is not looked up", in: model.MaintainerInput{Name: name, Email: "ada@localhost"}, unchanged: true},
		{name: "name at limit in characters", in: model.MaintainerInput{Name: multibyte(100), Email: mail}, unchanged: true},
		{name: "length is checked after trimming",
			in:   model.MaintainerInput{Name: "  " + multibyte(100) + "\t", Email: mail},
			want: model.MaintainerInput{Name: multibyte(100), Email: mail}},
		{name: "email at limit in characters",
			in:        model.MaintainerInput{Name: name, Email: multibyte(254-len(emailDomain)) + emailDomain},
			unchanged: true},

		{name: "name missing", in: model.MaintainerInput{Email: mail},
			wantErrs: []FieldError{{"name", "is required"}}},
		{name: "name only whitespace", in: model.MaintainerInput{Name: " \t ", Email: mail},
			wantErrs: []FieldError{{"name", "is required"}}},
		{name: "name over limit", in: model.MaintainerInput{Name: multibyte(101), Email: mail},
			wantErrs: []FieldError{{"name", "must be at most 100 characters"}}},
		{name: "email missing", in: model.MaintainerInput{Name: name, Email: "  "},
			wantErrs: []FieldError{{"email", "is required"}}},
		{name: "email over limit",
			in:       model.MaintainerInput{Name: name, Email: multibyte(255-len(emailDomain)) + emailDomain},
			wantErrs: []FieldError{{"email", "must be at most 254 characters"}}},
		{name: "both invalid, in field order", in: model.MaintainerInput{},
			wantErrs: []FieldError{{"name", "is required"}, {"email", "is required"}}},
	}
	for _, bad := range []string{
		"Ada <ada@example.com>", // display name
		"<ada@example.com>",     // angle brackets
		"ada@example.com (Ada)", // comment
		`"ada l"@example.com`,   // quoted local part
		"ada.example.com",       // no @
		"ada@@example.com",
		"ada@",
		"@example.com",
		"a..b@example.com",
		"ada@example.com, bob@example.com",
	} {
		tests = append(tests, maintainerTest{
			name:     "not a bare address: " + bad,
			in:       model.MaintainerInput{Name: name, Email: bad},
			wantErrs: []FieldError{{"email", "must be a bare email address such as name@example.com"}},
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := Maintainer(tt.in)
			if !slices.Equal(errs, tt.wantErrs) {
				t.Fatalf("errors = %v, want %v", errs, tt.wantErrs)
			}
			want := tt.want
			if tt.unchanged {
				want = tt.in
			}
			if len(tt.wantErrs) == 0 && got != want {
				t.Errorf("normalized = %+v, want %+v", got, want)
			}
		})
	}
}

type applicationTest struct {
	name      string
	in        model.ApplicationInput
	unchanged bool                   // the normalized copy equals in
	want      model.ApplicationInput // the normalized copy otherwise
	wantErrs  []FieldError           // the copy is not checked when set
}

var validApplication = model.ApplicationInput{
	MaintainerID: "8d3c4b8e-6c1a-4f2e-9d1b-2a7f0c9e5b11",
	Title:        "Valet",
	Version:      "1.4.2",
	Company:      "Example Corp",
	Website:      "https://example.com",
	Source:       "https://github.com/example/valet",
	License:      "Apache-2.0",
	Description:  "A service that parks cars.",
}

// withApp returns validApplication changed by f.
func withApp(f func(*model.ApplicationInput)) model.ApplicationInput {
	in := validApplication
	f(&in)
	return in
}

// applicationFields lists every field except maintainerId, so that each one
// is tested on its own: a validator that checks the wrong field fails.
var applicationFields = []struct {
	json  string
	max   int
	isURL bool
	set   func(*model.ApplicationInput, string)
}{
	{"title", 200, false, func(in *model.ApplicationInput, v string) { in.Title = v }},
	{"version", 64, false, func(in *model.ApplicationInput, v string) { in.Version = v }},
	{"company", 200, false, func(in *model.ApplicationInput, v string) { in.Company = v }},
	{"website", 2048, true, func(in *model.ApplicationInput, v string) { in.Website = v }},
	{"source", 2048, true, func(in *model.ApplicationInput, v string) { in.Source = v }},
	{"license", 64, false, func(in *model.ApplicationInput, v string) { in.License = v }},
	{"description", 4096, false, func(in *model.ApplicationInput, v string) { in.Description = v }},
}

func TestCreateApplication(t *testing.T) {
	minimal := model.ApplicationInput{MaintainerID: "m1", Title: "t", Version: "v"}
	tests := []applicationTest{
		{name: "valid", in: validApplication, unchanged: true},
		{name: "only required fields", in: minimal, unchanged: true},
		{name: "fields are trimmed",
			in: model.ApplicationInput{MaintainerID: " m1 ", Title: "\tt\n", Version: " v　", Company: " c ",
				Website: " http://x.example ", Source: " http://y.example ", License: " MIT ", Description: " d "},
			want: model.ApplicationInput{MaintainerID: "m1", Title: "t", Version: "v", Company: "c",
				Website: "http://x.example", Source: "http://y.example", License: "MIT", Description: "d"}},
		{name: "free-form version and license",
			in:        withApp(func(in *model.ApplicationInput) { in.Version, in.License = "2024.10-rc1+build.7", "Proprietary" }),
			unchanged: true},
		{name: "uppercase scheme",
			in:        withApp(func(in *model.ApplicationInput) { in.Website = "HTTPS://example.com/x" }),
			unchanged: true},

		{name: "maintainerId missing", in: withApp(func(in *model.ApplicationInput) { in.MaintainerID = " " }),
			wantErrs: []FieldError{{"maintainerId", "is required"}}},
		{name: "title missing", in: withApp(func(in *model.ApplicationInput) { in.Title = "" }),
			wantErrs: []FieldError{{"title", "is required"}}},
		{name: "version only whitespace", in: withApp(func(in *model.ApplicationInput) { in.Version = "\t " }),
			wantErrs: []FieldError{{"version", "is required"}}},
		{name: "empty input, in field order", in: model.ApplicationInput{},
			wantErrs: []FieldError{{"maintainerId", "is required"}, {"title", "is required"}, {"version", "is required"}}},
	}

	for _, f := range applicationFields {
		// Values of exactly max and max+1 characters, multi-byte so that a
		// byte count would fail.
		atLimit, overLimit := multibyte(f.max), multibyte(f.max+1)
		if f.isURL {
			atLimit = urlPrefix + multibyte(f.max-len(urlPrefix))
			overLimit = urlPrefix + multibyte(f.max+1-len(urlPrefix))
		}
		tests = append(tests,
			applicationTest{
				name:      f.json + " at its limit",
				in:        withApp(func(in *model.ApplicationInput) { f.set(in, atLimit) }),
				unchanged: true,
			},
			applicationTest{
				name:     f.json + " one over its limit",
				in:       withApp(func(in *model.ApplicationInput) { f.set(in, overLimit) }),
				wantErrs: []FieldError{{f.json, fmt.Sprintf("must be at most %d characters", f.max)}},
			})
		if !f.isURL {
			continue
		}
		for _, bad := range []string{
			"ftp://example.com",
			"javascript:alert(1)",
			"mailto:ada@example.com",
			"http:example.com", // no host
			"http://",
			"http:///path",
			"http://:8080",
			"//example.com", // no scheme
			"/relative/path",
			"example.com",
			"http://exa mple.com",
		} {
			tests = append(tests, applicationTest{
				name:     f.json + " not an absolute http(s) URL: " + bad,
				in:       withApp(func(in *model.ApplicationInput) { f.set(in, bad) }),
				wantErrs: []FieldError{{f.json, "must be an absolute http or https URL"}},
			})
		}
	}

	runApplicationTests(t, CreateApplication, tests)
}

func TestUpdateApplication(t *testing.T) {
	tests := []applicationTest{
		{name: "maintainerId may be omitted",
			in:        withApp(func(in *model.ApplicationInput) { in.MaintainerID = "" }),
			unchanged: true},
		{name: "whitespace-only maintainerId counts as omitted",
			in:   withApp(func(in *model.ApplicationInput) { in.MaintainerID = " \t" }),
			want: withApp(func(in *model.ApplicationInput) { in.MaintainerID = "" })},
		{name: "given maintainerId is trimmed",
			in:   withApp(func(in *model.ApplicationInput) { in.MaintainerID = " m1 " }),
			want: withApp(func(in *model.ApplicationInput) { in.MaintainerID = "m1" })},
		{name: "other fields are still validated", in: model.ApplicationInput{},
			wantErrs: []FieldError{{"title", "is required"}, {"version", "is required"}}},
	}
	runApplicationTests(t, UpdateApplication, tests)
}

func runApplicationTests(t *testing.T, validate func(model.ApplicationInput) (model.ApplicationInput, []FieldError), tests []applicationTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := validate(tt.in)
			if !slices.Equal(errs, tt.wantErrs) {
				t.Fatalf("errors = %v, want %v", errs, tt.wantErrs)
			}
			want := tt.want
			if tt.unchanged {
				want = tt.in
			}
			if len(tt.wantErrs) == 0 && got != want {
				t.Errorf("normalized = %+v, want %+v", got, want)
			}
		})
	}
}
