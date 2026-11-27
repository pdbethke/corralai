// SPDX-License-Identifier: Elastic-2.0

package lang

// rubyGemfileSearchDepth bounds the upward walk for a Gemfile. Bundler
// itself walks to the filesystem root; no real checkout is nested this deep.
const rubyGemfileSearchDepth = 64

// Lockfile is Bundler's Gemfile.lock beside the Gemfile. A tree that holds
// no Gemfile.lock has Bundler resolve the Gemfile again among the gems the
// machine has: measured on Ruby 3.3.8 and Bundler 2.5.22, with rspec-core
// 3.13.0 and 3.13.6 both installed, `bundle exec rspec` ran 3.13.0 in a
// checkout whose Gemfile.lock pinned it and 3.13.6 in a copy without it.
//
// Not covered: a project that names its files gems.rb and gems.locked
// (Bundler's other spelling) has no Gemfile, so no lockfile rule, and its
// trees resolve for themselves as before; nor does BUNDLE_GEMFILE pointing
// elsewhere. A test command that does not go through Bundler at all reads no
// Gemfile.lock, so for it the rule only costs trees when the checkout has
// none to copy.
func (rubyPlugin) Lockfile() Lockfile {
	return Lockfile{Name: "Gemfile.lock", Manifest: "Gemfile", SearchDepth: rubyGemfileSearchDepth}
}

// LockfileDir is manifestDir: Bundler writes Gemfile.lock beside the Gemfile
// it resolved.
func (rubyPlugin) LockfileDir(manifestDir string) string { return manifestDir }

// ManifestPins is true for every Gemfile: it is Ruby code, not read here, so
// what it declares cannot be established, which counts as pinning.
func (rubyPlugin) ManifestPins(string) bool { return true }
