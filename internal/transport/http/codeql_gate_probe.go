package http

import (
	"net/http"
	"strconv"
)

// DeliberatelyUnboundedProbe is a throwaway. It exists on this branch only, to
// prove the merge gate added alongside #122 actually refuses a pull request
// that introduces a new high-severity CodeQL finding, and it is deleted rather
// than merged. `?n=` reaches make() with no ceiling, which is exactly the shape
// alert #5 turned out not to be.
func DeliberatelyUnboundedProbe(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	buf := make([]byte, n)
	_, _ = w.Write(buf)
}
