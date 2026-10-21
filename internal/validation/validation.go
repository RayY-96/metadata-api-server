// Package validation checks and normalizes client input (DESIGN.md §3.1).
package validation

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

// FieldError describes why one field is invalid. Field is the JSON name.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Maintainer returns a normalized copy of in (fields trimmed, email
// lower-cased) and the errors of every invalid field, in field order. The
// copy is meaningful only when there are no errors.
func Maintainer(in model.MaintainerInput) (model.MaintainerInput, []FieldError) {
	var errs []FieldError
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))

	errs = text(errs, "name", in.Name, true, 100)
	errs = email(errs, "email", in.Email)
	return in, errs
}

// CreateApplication validates the body of a create request, in which
// maintainerId is required. Like Maintainer, it returns a normalized copy and
// the field errors. Whether MaintainerID refers to an existing Maintainer is
// checked by the database.
func CreateApplication(in model.ApplicationInput) (model.ApplicationInput, []FieldError) {
	return application(in, true)
}

// UpdateApplication validates the body of an update request. An empty
// MaintainerID means it was omitted; whether a given one matches the stored
// value is checked against the stored Application.
func UpdateApplication(in model.ApplicationInput) (model.ApplicationInput, []FieldError) {
	return application(in, false)
}

func application(in model.ApplicationInput, requireMaintainer bool) (model.ApplicationInput, []FieldError) {
	var errs []FieldError
	in.MaintainerID = strings.TrimSpace(in.MaintainerID)
	in.Title = strings.TrimSpace(in.Title)
	in.Version = strings.TrimSpace(in.Version)
	in.Company = strings.TrimSpace(in.Company)
	in.Website = strings.TrimSpace(in.Website)
	in.Source = strings.TrimSpace(in.Source)
	in.License = strings.TrimSpace(in.License)
	in.Description = strings.TrimSpace(in.Description)

	if requireMaintainer && in.MaintainerID == "" {
		errs = append(errs, required("maintainerId"))
	}
	errs = text(errs, "title", in.Title, true, 200)
	errs = text(errs, "version", in.Version, true, 64)
	errs = text(errs, "company", in.Company, false, 200)
	errs = webURL(errs, "website", in.Website)
	errs = webURL(errs, "source", in.Source)
	errs = text(errs, "license", in.License, false, 64)
	errs = text(errs, "description", in.Description, false, 4096)
	return in, errs
}

func required(field string) FieldError { return FieldError{field, "is required"} }

func tooLong(field string, maxLen int) FieldError {
	return FieldError{field, fmt.Sprintf("must be at most %d characters", maxLen)}
}

// text checks a trimmed string field. Lengths are counted in Unicode code
// points, not bytes.
func text(errs []FieldError, field, v string, isRequired bool, maxLen int) []FieldError {
	switch {
	case v == "" && isRequired:
		return append(errs, required(field))
	case utf8.RuneCountInString(v) > maxLen:
		return append(errs, tooLong(field, maxLen))
	}
	return errs
}

// email accepts a bare address such as name@example.com. A display name,
// angle brackets or a comment ("Name <a@b.com>", "<a@b.com>", "a@b.com (x)")
// make the parsed address differ from the input, so they are rejected.
func email(errs []FieldError, field, v string) []FieldError {
	if v == "" {
		return append(errs, required(field))
	}
	if utf8.RuneCountInString(v) > 254 {
		return append(errs, tooLong(field, 254))
	}
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Address != v {
		return append(errs, FieldError{field, "must be a bare email address such as name@example.com"})
	}
	return errs
}

// webURL checks an optional absolute http or https URL with a host.
func webURL(errs []FieldError, field, v string) []FieldError {
	if v == "" {
		return errs
	}
	if utf8.RuneCountInString(v) > 2048 {
		return append(errs, tooLong(field, 2048))
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return append(errs, FieldError{field, "must be an absolute http or https URL"})
	}
	return errs
}
