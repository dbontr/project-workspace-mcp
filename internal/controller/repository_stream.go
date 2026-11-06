package controller

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SnapshotReader returns an immutable GitHub archive as a stream. The first
// request authenticates only to GitHub; the redirected archive request never
// carries a repository credential.
func (g *GitHub) SnapshotReader(repo, ref string) (string, io.ReadCloser, error) {
	prefix, e := repoPath(repo)
	if e != nil {
		return "", nil, e
	}
	sha, e := g.Commit(repo, ref)
	if e != nil {
		return "", nil, e
	}
	api, e := url.Parse(g.API)
	if e != nil {
		return "", nil, e
	}
	token, e := g.credential(prefix + "/tarball/" + sha)
	if e != nil {
		return "", nil, e
	}
	firstURL := g.API + prefix + "/tarball/" + sha
	target, e := url.Parse(firstURL)
	if e != nil {
		return "", nil, e
	}
	if api.Host != target.Host || api.Scheme != target.Scheme {
		return "", nil, errors.New("untrusted GitHub archive API host")
	}
	req, e := http.NewRequest("GET", target.String(), nil)
	if e != nil {
		return "", nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "workspace-mcp")
	client := *g.Client
	client.Timeout = 60 * time.Minute
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	first, e := client.Do(req)
	if e != nil {
		return "", nil, e
	}
	first.Body.Close()
	if first.StatusCode != http.StatusFound && first.StatusCode != http.StatusTemporaryRedirect && first.StatusCode != http.StatusMovedPermanently {
		return "", nil, fmt.Errorf("GitHub archive returned HTTP %d", first.StatusCode)
	}
	location, e := url.Parse(first.Header.Get("Location"))
	if e != nil || location == nil || location.Scheme != "https" || location.User != nil || location.Port() != "" ||
		(location.Hostname() != "codeload.github.com" && location.Hostname() != "api.github.com") ||
		strings.HasPrefix(location.Path, "//") {
		return "", nil, errors.New("untrusted GitHub archive redirect")
	}
	download, e := http.NewRequest("GET", location.String(), nil)
	if e != nil {
		return "", nil, e
	}
	download.Header.Set("User-Agent", "workspace-mcp")
	response, e := client.Do(download)
	if e != nil {
		return "", nil, e
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return "", nil, fmt.Errorf("GitHub archive download returned HTTP %d", response.StatusCode)
	}
	return sha, response.Body, nil
}

// SourceSnapshotReader returns a verified, immutable gzip-compressed Git
// archive from either GitHub or an operator-mounted local bare repository.
func (s *Server) SourceSnapshotReader(repository, ref string) (string, io.ReadCloser, error) {
	name, local := localRepoName(repository)
	if !local {
		if strings.HasPrefix(repository, "local:") {
			return "", nil, errors.New("invalid local Git repository")
		}
		return s.GitHub.SnapshotReader(repository, ref)
	}
	if s.LocalGit == nil {
		return "", nil, errors.New("local Git storage is not configured")
	}
	sha, err := s.LocalGit.Resolve(name, ref)
	if err != nil {
		return "", nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		gzipWriter := gzip.NewWriter(writer)
		_, err := s.LocalGit.Archive(name, sha, gzipWriter)
		if closeErr := gzipWriter.Close(); err == nil {
			err = closeErr
		}
		writer.CloseWithError(err)
	}()
	return sha, reader, nil
}

func (s *Server) CheckoutArchive(project, workspace, repo, ref string) (map[string]any, error) {
	if !s.DB.RepositoryLinked(project, repo) {
		return nil, errors.New("repository is not linked to project")
	}
	sha, reader, e := s.SourceSnapshotReader(repo, ref)
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	meta, e := s.DB.StoreArtifactStream(project, "repo-"+sha[:12]+".tar.gz", reader, s.MaxArtifactBytes)
	if e != nil {
		return nil, e
	}
	op, e := s.DB.QueueImportRepository(project, workspace, repo, sha, asString(meta["id"]))
	if e != nil {
		return nil, e
	}
	op["revision"] = sha
	op["artifact_id"] = meta["id"]
	return op, nil
}
func (s *Server) CheckoutLibrarySource(project, workspace, library, repo, path string) (map[string]any, error) {
	if !s.DB.libraryAttached(project, library) {
		return nil, errors.New("library is not linked to project")
	}
	source, e := s.DB.LibrarySource(library, repo, path)
	if e != nil {
		return nil, e
	}
	sha := asString(source["revision"])
	confirmed, reader, e := s.SourceSnapshotReader(repo, sha)
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	if confirmed != sha {
		return nil, errors.New("library source revision changed")
	}
	meta, e := s.DB.StoreArtifactStream(project, "library-"+sha[:12]+".tar.gz", reader, s.MaxArtifactBytes)
	if e != nil {
		return nil, e
	}
	op, e := s.DB.QueueImportLibraryRepository(project, workspace, library, repo, path, sha, asString(meta["id"]))
	if e != nil {
		return nil, e
	}
	op["revision"] = sha
	op["artifact_id"] = meta["id"]
	op["library"] = library
	return op, nil
}
