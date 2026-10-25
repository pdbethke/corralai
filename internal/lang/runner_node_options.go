// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// nodeOptionsFrom is the node whose `node --help` the two tables below were
// read from (TestNodeOptionTableMatchesNodeHelp re-reads it when that node
// is on PATH).
const nodeOptionsFrom = "v22.23."

// nodeValueOptions are node's options that take a value, which node reads
// from the next word when it is not written with "=" (`--import tsx`):
// every option `node --help` prints with "=...", aliases included. One
// printed with "[=...]" takes its value only after "=" and is in
// nodeFlagOptions.
var nodeValueOptions = optionSet(`
--allow-fs-read --allow-fs-write --build-snapshot-config --conditions
--cpu-prof-dir --cpu-prof-interval --cpu-prof-name --debug-port
--diagnostic-dir --disable-proto --disable-warning --dns-result-order
--env-file --env-file-if-exists --eval --experimental-config-file
--experimental-default-type --experimental-loader
--experimental-sea-config --experimental-test-isolation
--heap-prof-dir --heap-prof-interval --heap-prof-name
--heapsnapshot-near-heap-limit --heapsnapshot-signal --icu-data-dir
--import --input-type --inspect-port --inspect-publish-uid --loader
--localstorage-file --max-http-header-size
--max-old-space-size-percentage
--network-family-autoselection-attempt-timeout --openssl-config
--redirect-warnings --report-dir --report-directory --report-filename
--report-signal --require --run --secure-heap --secure-heap-min
--snapshot-blob --test-concurrency --test-coverage-branches
--test-coverage-exclude --test-coverage-functions
--test-coverage-include --test-coverage-lines --test-name-pattern
--test-reporter --test-reporter-destination --test-shard
--test-skip-pattern --test-timeout --title --tls-cipher-list
--tls-keylog --trace-event-categories --trace-event-file-pattern
--trace-require-module --unhandled-rejections --use-largepages
--v8-pool-size --watch-kill-signal --watch-path -C -e -r`)

// nodeFlagOptions are the rest of `node --help`: options that never read the
// next word. A --no- form is node's negation of the option it names. V8's
// own options are in neither table; node reads their values only after "="
// (measured on node 22.23: `node --stack-size 2000` is "illegal value"), so
// an unknown flag followed by a bare word is refused, never guessed.
var nodeFlagOptions = optionSet(`
--abort-on-uncaught-exception --allow-addons --allow-child-process
--allow-wasi --allow-worker --build-snapshot --check
--completion-bash --cpu-prof --disable-sigusr1
--disable-wasm-trap-handler --disallow-code-generation-from-strings
--enable-etw-stack-walking --enable-fips
--enable-network-family-autoselection --enable-source-maps
--entry-url --experimental-addon-modules
--experimental-async-context-frame --experimental-default-config-file
--experimental-eventsource --experimental-import-meta-resolve
--experimental-inspector-network-resource
--experimental-network-inspection --experimental-permission
--experimental-print-required-tla --experimental-test-coverage
--experimental-test-module-mocks --experimental-transform-types
--experimental-vm-modules --experimental-webstorage
--experimental-worker-inspection --expose-gc --force-context-aware
--force-fips --force-node-api-uncaught-exceptions-policy
--frozen-intrinsics --heap-prof --help --huge-max-old-generation-size
--insecure-http-parser --inspect --inspect-brk --inspect-wait
--interactive --interpreted-frames-native-stack --jitless --no-addons
--no-deprecation --no-experimental-detect-module
--no-experimental-fetch --no-experimental-global-customevent
--no-experimental-global-navigator --no-experimental-global-webcrypto
--no-experimental-repl-await --no-experimental-require-module
--no-experimental-sqlite --no-experimental-strip-types
--no-experimental-websocket --no-extra-info-on-fatal-exception
--no-force-async-hooks-checks --no-global-search-paths
--no-network-family-autoselection --no-warnings --node-memory-debug
--openssl-legacy-provider --openssl-shared-config
--pending-deprecation --permission --preserve-symlinks
--preserve-symlinks-main --print --prof --prof-process
--report-compact --report-exclude-env --report-exclude-network
--report-on-fatalerror --report-on-signal --report-uncaught-exception
--test --test-force-exit --test-only --test-update-snapshots
--throw-deprecation --tls-max-v1.2 --tls-max-v1.3 --tls-min-v1.0
--tls-min-v1.1 --tls-min-v1.2 --tls-min-v1.3 --trace-atomics-wait
--trace-deprecation --trace-env --trace-env-js-stack
--trace-env-native-stack --trace-exit --trace-promises --trace-sigint
--trace-sync-io --trace-tls --trace-uncaught --trace-warnings
--track-heap-objects --use-bundled-ca --use-env-proxy
--use-openssl-ca --use-system-ca --v8-options --version --watch
--watch-preserve-output --zero-fill-buffers -c -h -i -p -v`)

func optionSet(words string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(words) {
		set[w] = true
	}
	return set
}

// nodeOptionTakesValue is dropFileArgs' answer for node: whether flag reads
// the next word, and whether node lists it at all.
func nodeOptionTakesValue(flag string) (value, known bool) {
	if nodeValueOptions[flag] {
		return true, true
	}
	if nodeFlagOptions[flag] {
		return false, true
	}
	if rest, ok := strings.CutPrefix(flag, "--no-"); ok && nodeFlagOptions["--"+rest] {
		return false, true
	}
	if rest, ok := strings.CutPrefix(flag, "--"); ok && nodeFlagOptions["--no-"+rest] {
		return false, true
	}
	return false, false
}
