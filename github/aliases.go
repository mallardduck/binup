package github

import ghrelease "github.com/mallardduck/ghreleases"

// This file re-exports the ghreleases config-value types/constants that
// github.Source's Options accept (TemplateMode, ArchiveFormat), as type
// aliases rather than defined types — identical to the underlying ghreleases
// type at compile time, not a conversion. That means configuring a
// github.Source (WithTemplateMode(github.TemplateStrict),
// WithExtractFormat(github.FormatTarGz), ...) never requires importing
// ghreleases directly: it stays an indirect dependency, pulled in only by
// this package, exactly like binup core's own zero dependency on ghreleases.

// TemplateMode controls how {variable} tokens are rendered in asset/checksum
// name templates (see WithTemplateMode).
type TemplateMode = ghrelease.TemplateMode

const (
	// TemplateStrict returns an error on an unrecognized template variable or
	// modifier instead of rendering it as literal text.
	TemplateStrict = ghrelease.TemplateStrict
	// TemplatePermissive keeps an unrecognized template variable/modifier as
	// literal text instead of erroring. Default.
	TemplatePermissive = ghrelease.TemplatePermissive
	// TemplateFailsafe ignores unknown modifiers (keeping the variable's
	// rendered value) but still preserves unknown variables as literal text.
	TemplateFailsafe = ghrelease.TemplateFailsafe
)

// ArchiveFormat forces a specific archive format instead of relying on
// filename-suffix auto-detection (see WithExtractFormat).
type ArchiveFormat = ghrelease.ArchiveFormat

const (
	FormatTarGz ArchiveFormat = ghrelease.FormatTarGz
	FormatTgz   ArchiveFormat = ghrelease.FormatTgz
	FormatZip   ArchiveFormat = ghrelease.FormatZip
	FormatGzip  ArchiveFormat = ghrelease.FormatGzip
	FormatPlain ArchiveFormat = ghrelease.FormatPlain
)
