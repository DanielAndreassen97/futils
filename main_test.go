package main

import (
	"testing"
	"time"
)

func TestFormatVersion(t *testing.T) {
	buildTime := time.Date(2026, 5, 29, 15, 4, 5, 0, time.UTC)

	cases := []struct {
		name     string
		version  string
		built    time.Time
		revision string
		modified bool
		want     string
	}{
		{
			name:     "release version prints plainly with no build details",
			version:  "v1.2.3",
			built:    buildTime,
			revision: "abc1234def",
			modified: true,
			want:     "futils v1.2.3",
		},
		{
			name:     "dev build shows build time and short dirty revision",
			version:  "dev",
			built:    buildTime,
			revision: "9a54fc55c8ccdaab62e93acf2639587fcbc58aa3",
			modified: true,
			want:     "futils dev\n  built:    2026-05-29 15:04:05\n  revision: 9a54fc5 (modified)",
		},
		{
			name:     "dev build with clean revision omits the modified marker",
			version:  "dev",
			built:    buildTime,
			revision: "9a54fc55c8ccdaab62e93acf2639587fcbc58aa3",
			modified: false,
			want:     "futils dev\n  built:    2026-05-29 15:04:05\n  revision: 9a54fc5",
		},
		{
			name:     "dev build with no build info falls back to bare dev",
			version:  "dev",
			built:    time.Time{},
			revision: "",
			modified: false,
			want:     "futils dev",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatVersion(tc.version, tc.built, tc.revision, tc.modified)
			if got != tc.want {
				t.Errorf("formatVersion(%q, ...):\n got: %q\nwant: %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestBannerBuildInfo(t *testing.T) {
	buildTime := time.Date(2026, 5, 29, 16, 13, 0, 0, time.UTC)

	cases := []struct {
		name     string
		version  string
		built    time.Time
		revision string
		modified bool
		want     string
	}{
		{
			name:     "release build shows no build line in the banner",
			version:  "v1.2.3",
			built:    buildTime,
			revision: "abc1234",
			modified: true,
			want:     "",
		},
		{
			name:     "dev dirty build is a compact one-liner with a star",
			version:  "dev",
			built:    buildTime,
			revision: "9a54fc55c8ccdaab62e93acf2639587fcbc58aa3",
			modified: true,
			want:     "built 2026-05-29 16:13 · 9a54fc5*",
		},
		{
			name:     "dev clean build drops the star",
			version:  "dev",
			built:    buildTime,
			revision: "9a54fc55c8ccdaab62e93acf2639587fcbc58aa3",
			modified: false,
			want:     "built 2026-05-29 16:13 · 9a54fc5",
		},
		{
			name:     "dev build with no provenance is empty",
			version:  "dev",
			built:    time.Time{},
			revision: "",
			modified: false,
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bannerBuildInfo(tc.version, tc.built, tc.revision, tc.modified)
			if got != tc.want {
				t.Errorf("bannerBuildInfo(%q, ...) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}
