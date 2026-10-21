// Package model defines the API resources (DESIGN.md §3.1).
package model

import "time"

// Maintainer is a person who creates Applications.
type Maintainer struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Email           string    `json:"email"`
	ResourceVersion int64     `json:"resourceVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// MaintainerInput holds the fields of a Maintainer that clients may write.
// It is the request body of POST and PUT.
type MaintainerInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Application is a piece of software and its metadata. MaintainerID refers to
// the Maintainer who created it and never changes.
type Application struct {
	ID              string    `json:"id"`
	MaintainerID    string    `json:"maintainerId"`
	Title           string    `json:"title"`
	Version         string    `json:"version"`
	Company         string    `json:"company"`
	Website         string    `json:"website"`
	Source          string    `json:"source"`
	License         string    `json:"license"`
	Description     string    `json:"description"`
	ResourceVersion int64     `json:"resourceVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ApplicationInput holds the fields of an Application that clients may
// write. It is the request body of POST and PUT.
type ApplicationInput struct {
	MaintainerID string `json:"maintainerId"`
	Title        string `json:"title"`
	Version      string `json:"version"`
	Company      string `json:"company"`
	Website      string `json:"website"`
	Source       string `json:"source"`
	License      string `json:"license"`
	Description  string `json:"description"`
}
