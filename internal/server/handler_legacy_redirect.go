package server

import (
	"net/http"
	"strings"

	"github.com/tjst-t/palmux2/internal/store"
)

// legacyBranchIDRedirect (S1e8d02) wraps an /api/* request multiplexer
// so that any request whose path contains `/branches/{legacyId}/...`
// (the pre-S1e8d02 branch-name-based ID) is responded to with a 302
// pointing at the new path-based ID. The redirect is server-side so
// existing bookmarks, shared chat URLs, and stale FE state recover
// without a roundtrip via the SPA.
//
// The match is token-based: we split on `/`, look for the literal
// `branches` segment, and try to resolve the next token as a legacy
// branch ID for the repo identified by the segment before `repos/`.
// Anything we don't recognise passes through to `next`.
//
// The wrapper is intentionally non-strict — if the lookup throws or
// the path doesn't fit the expected shape, we fall through to the
// underlying handler which returns its own 404. That keeps the cost
// of the wrapper bounded and avoids accidentally swallowing requests
// that legitimately have nothing to do with the branch-id namespace.
//
// The token-based parser handles four URL shapes:
//   - `/api/repos/{repoId}/branches/{branchId}` (close branch)
//   - `/api/repos/{repoId}/branches/{branchId}/...` (most everything)
//   - SPA-fallback `/{repoId}/{branchId}/...` is not handled here
//     (the SPA does its own client-side legacy resolve via
//      `ResolveLegacyBranchID` exposed through GET /api/repos)
func legacyBranchIDRedirect(st *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodDelete && r.Method != http.MethodPatch &&
			r.Method != http.MethodPost && r.Method != http.MethodPut {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/repos/") {
			next.ServeHTTP(w, r)
			return
		}
		// Tokenise: after TrimPrefix("/") + Split("/"), the path
		// `api/repos/{repoId}/branches/{branchId}` has 5 tokens at
		// indices [0..4]. The branch sub-routes (e.g.
		// `.../branches/{branchId}/tabs/...`) extend beyond.
		//
		// Index map: 0=api 1=repos 2={repoId} 3=branches 4={branchId}
		toks := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(toks) < 5 {
			next.ServeHTTP(w, r)
			return
		}
		if toks[0] != "api" || toks[1] != "repos" || toks[3] != "branches" {
			next.ServeHTTP(w, r)
			return
		}
		repoID := toks[2]
		idTok := toks[4]
		// Skip if the ID is already valid (no resolution needed). We
		// only consult the legacy-resolver when the live branch lookup
		// would fail, so the hot path is one map lookup.
		if _, err := st.Branch(repoID, idTok); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		newID, ok := st.ResolveLegacyBranchID(repoID, idTok)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		// Rewrite tok and redirect.
		toks[4] = newID
		newPath := "/" + strings.Join(toks, "/")
		if r.URL.RawQuery != "" {
			newPath += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", newPath)
		// Use 302 for GET/HEAD (the common case — bookmarked URL)
		// per the S1e8d02 roadmap. For non-idempotent methods (POST,
		// PUT, PATCH, DELETE) 307 preserves the method + body so the
		// retry actually does what the caller meant. The wire status
		// in either case satisfies AC-S1e8d02-3-2 ("302 redirect")
		// when the caller is reading a stale GET URL; the 307
		// upgrade for write methods is the spec-correct refinement.
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			w.WriteHeader(http.StatusFound) // 302
		default:
			w.WriteHeader(http.StatusTemporaryRedirect) // 307
		}
	})
}
