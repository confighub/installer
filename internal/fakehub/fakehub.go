// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package fakehub is an in-memory stand-in for the parts of the ConfigHub API
// the installer calls, for tests that drive setup, plan and upload without a
// server. It serves what cubapi's list, resolve, upload, unit-data and
// unit-patch helpers send, logs every request, and simulates an upload closely
// enough that what one upload writes is what the next reads back: each
// uploaded file becomes one Unit, and the installer record can be fetched to
// re-enter the install.
//
// IDs are sequential, so a test's requests and output are the same every run.
package fakehub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/google/uuid"
)

// Request is one request the server received.
type Request struct {
	Method string
	Path   string // without the /api prefix
	Query  map[string]string
	Body   []byte
}

// Unit is a stored Unit and its configuration data.
type Unit struct {
	goclientnew.Unit
	Data string
}

// UploadHook decides an upload's result in place of the simulation. It gets
// the server's simulated result, which it may change or replace.
type UploadHook func(req goclientnew.UploadRequest, dryRun bool, simulated *goclientnew.UploadResult) *goclientnew.UploadResult

// Server is a fake ConfigHub API server.
type Server struct {
	*httptest.Server

	mu         sync.Mutex
	nextID     int
	spaces     []*goclientnew.Space
	components []*goclientnew.Component
	targets    []*goclientnew.Target
	changeSets []*goclientnew.ChangeSet
	units      []*Unit
	requests   []Request
	uploadHook UploadHook
	refuseAuth bool
	refuse     func(req goclientnew.UploadRequest, dryRun bool) bool
}

// OrganizationID is the organization every entity belongs to.
var OrganizationID = goclientnew.UUID(uuid.MustParse("00000000-0000-0000-0000-00000000000f"))

// New starts a server. Close it when done.
func New() *Server {
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Reset forgets every entity and request, and the upload hook.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID = 0
	s.spaces, s.components, s.targets, s.changeSets, s.units = nil, nil, nil, nil, nil
	s.requests = nil
	s.uploadHook = nil
	s.refuseAuth = false
	s.refuse = nil
}

// RefuseUploads makes the uploads refuse says to refuse fail outright, as
// ConfigHub does for a request it rejects; nil refuses none.
func (s *Server) RefuseUploads(refuse func(req goclientnew.UploadRequest, dryRun bool) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = refuse
}

// RefuseAuth makes every request fail as unauthenticated, as a server does
// for an expired token.
func (s *Server) RefuseAuth(refuse bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseAuth = refuse
}

// SetUploadHook sets the hook that decides upload results; nil restores the
// simulation.
func (s *Server) SetUploadHook(h UploadHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadHook = h
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// ClearRequests forgets the requests received so far.
func (s *Server) ClearRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *Server) newID() goclientnew.UUID {
	s.nextID++
	return goclientnew.UUID(uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", s.nextID)))
}

// AddSpace adds a Space and returns it.
func (s *Server) AddSpace(slug string, labels, annotations map[string]string) *goclientnew.Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addSpace(slug, labels, annotations)
}

func (s *Server) addSpace(slug string, labels, annotations map[string]string) *goclientnew.Space {
	sp := &goclientnew.Space{SpaceID: s.newID(), Slug: slug, OrganizationID: OrganizationID,
		Labels: copyMap(labels), Annotations: copyMap(annotations)}
	s.spaces = append(s.spaces, sp)
	return sp
}

// AddComponent adds a Component and returns it.
func (s *Server) AddComponent(slug string) *goclientnew.Component {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addComponent(slug)
}

func (s *Server) addComponent(slug string) *goclientnew.Component {
	c := &goclientnew.Component{ComponentID: s.newID(), Slug: slug, OrganizationID: OrganizationID}
	s.components = append(s.components, c)
	return c
}

// AddTarget adds a Target to a Space and returns it.
func (s *Server) AddTarget(space *goclientnew.Space, slug string) *goclientnew.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &goclientnew.Target{TargetID: s.newID(), Slug: slug, SpaceID: space.SpaceID, SpaceSlug: space.Slug, OrganizationID: OrganizationID}
	s.targets = append(s.targets, t)
	return t
}

// AddChangeSet adds an open ChangeSet to a Space and returns it.
func (s *Server) AddChangeSet(space *goclientnew.Space, slug string) *goclientnew.ChangeSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addChangeSet(space.SpaceID, space.Slug, slug)
}

func (s *Server) addChangeSet(spaceID goclientnew.UUID, spaceSlug, slug string) *goclientnew.ChangeSet {
	cs := &goclientnew.ChangeSet{ChangeSetID: s.newID(), Slug: slug, SpaceID: spaceID, SpaceSlug: spaceSlug, OrganizationID: OrganizationID, State: "Open"}
	s.changeSets = append(s.changeSets, cs)
	return cs
}

// Space returns the Space with slug, or nil.
func (s *Server) Space(slug string) *goclientnew.Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spaceBySlug(slug)
}

func (s *Server) spaceBySlug(slug string) *goclientnew.Space {
	for _, sp := range s.spaces {
		if sp.Slug == slug {
			return sp
		}
	}
	return nil
}

// Units returns the Units in the Space with slug, in creation order.
func (s *Server) Units(spaceSlug string) []Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Unit
	for _, u := range s.units {
		if u.SpaceSlug == spaceSlug {
			out = append(out, *u)
		}
	}
	return out
}

// Components returns the Components' slugs.
func (s *Server) Components() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.components {
		out = append(out, c.Slug)
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p := strings.TrimPrefix(r.URL.Path, "/api")
	query := map[string]string{}
	for k, v := range r.URL.Query() {
		query[k] = strings.Join(v, ",")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: p, Query: query, Body: body})

	if s.refuseAuth {
		writeError(w, http.StatusUnauthorized, "token expired")
		return
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case r.Method == http.MethodGet && p == "/me":
		writeJSON(w, http.StatusOK, goclientnew.OrganizationMember{OrganizationID: OrganizationID, Username: "test"})
	case r.Method == http.MethodGet && p == "/space":
		s.listSpaces(w, query)
	case r.Method == http.MethodGet && p == "/component":
		s.listComponents(w, query)
	case r.Method == http.MethodGet && p == "/target":
		s.listTargets(w, query)
	case r.Method == http.MethodGet && p == "/change_set":
		s.listChangeSets(w, query)
	case r.Method == http.MethodGet && p == "/unit":
		s.listUnits(w, query)
	case r.Method == http.MethodPost && p == "/upload":
		s.upload(w, query, body)
	case len(segments) == 4 && segments[0] == "space" && segments[2] == "unit" && r.Method == http.MethodPatch:
		s.patchUnit(w, segments[3], body)
	case len(segments) == 5 && segments[0] == "space" && segments[2] == "unit" && segments[4] == "data" && r.Method == http.MethodGet:
		s.unitData(w, segments[3])
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("fakehub: no handler for %s %s", r.Method, p))
	}
}

// clause is one `Field = 'value'` term of a where expression.
type clause struct{ field, value string }

var clauseRE = regexp.MustCompile(`^\s*([A-Za-z0-9_.]+)\s*=\s*'([^']*)'\s*$`)

// parseWhere parses the conjunction of equality terms cubapi.Where builds.
func parseWhere(where string) ([]clause, error) {
	if strings.TrimSpace(where) == "" {
		return nil, nil
	}
	var out []clause
	for _, term := range strings.Split(where, " AND ") {
		m := clauseRE.FindStringSubmatch(term)
		if m == nil {
			return nil, fmt.Errorf("fakehub: unsupported where term %q", term)
		}
		out = append(out, clause{m[1], m[2]})
	}
	return out, nil
}

// matches reports whether every clause holds for an entity, given a lookup of
// its plain fields, labels and annotations.
func matches(clauses []clause, field func(string) (string, bool), labels, annotations map[string]string) bool {
	for _, c := range clauses {
		var got string
		var ok bool
		switch {
		case strings.HasPrefix(c.field, "Labels."):
			got, ok = labels[strings.TrimPrefix(c.field, "Labels.")]
		case strings.HasPrefix(c.field, "Annotations."):
			got, ok = annotations[strings.TrimPrefix(c.field, "Annotations.")]
		default:
			got, ok = field(c.field)
		}
		if !ok || got != c.value {
			return false
		}
	}
	return true
}

func (s *Server) listSpaces(w http.ResponseWriter, q map[string]string) {
	clauses, err := parseWhere(q["where"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := []goclientnew.ExtendedSpace{}
	for _, sp := range s.spaces {
		field := func(f string) (string, bool) {
			switch f {
			case "Slug":
				return sp.Slug, true
			case "SpaceID":
				return sp.SpaceID.String(), true
			}
			return "", false
		}
		if matches(clauses, field, sp.Labels, sp.Annotations) {
			out = append(out, goclientnew.ExtendedSpace{Space: sp})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listComponents(w http.ResponseWriter, q map[string]string) {
	clauses, err := parseWhere(q["where"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := []goclientnew.ExtendedComponent{}
	for _, c := range s.components {
		field := func(f string) (string, bool) {
			switch f {
			case "Slug":
				return c.Slug, true
			case "ComponentID":
				return c.ComponentID.String(), true
			}
			return "", false
		}
		if matches(clauses, field, c.Labels, c.Annotations) {
			out = append(out, goclientnew.ExtendedComponent{Component: c})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listTargets(w http.ResponseWriter, q map[string]string) {
	clauses, err := parseWhere(q["where"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := []goclientnew.ExtendedTarget{}
	for _, t := range s.targets {
		field := func(f string) (string, bool) {
			switch f {
			case "Slug":
				return t.Slug, true
			case "TargetID":
				return t.TargetID.String(), true
			case "SpaceID":
				return t.SpaceID.String(), true
			}
			return "", false
		}
		if matches(clauses, field, t.Labels, t.Annotations) {
			out = append(out, goclientnew.ExtendedTarget{Target: t})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listChangeSets(w http.ResponseWriter, q map[string]string) {
	clauses, err := parseWhere(q["where"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := []goclientnew.ExtendedChangeSet{}
	for _, cs := range s.changeSets {
		field := func(f string) (string, bool) {
			switch f {
			case "Slug":
				return cs.Slug, true
			case "ChangeSetID":
				return cs.ChangeSetID.String(), true
			case "SpaceID":
				return cs.SpaceID.String(), true
			}
			return "", false
		}
		if matches(clauses, field, cs.Labels, cs.Annotations) {
			out = append(out, goclientnew.ExtendedChangeSet{ChangeSet: cs})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listUnits(w http.ResponseWriter, q map[string]string) {
	clauses, err := parseWhere(q["where"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hidden := map[string]bool{}
	for _, reason := range strings.Split(q["include_hidden"], ",") {
		if reason != "" {
			hidden[reason] = true
		}
	}
	includeSpace := false
	for _, inc := range strings.Split(q["include"], ",") {
		if inc == "SpaceID" {
			includeSpace = true
		}
	}
	out := []goclientnew.ExtendedUnit{}
	for _, u := range s.units {
		if u.HiddenReason != "" && !hidden["*"] && !hidden[u.HiddenReason] {
			continue
		}
		field := func(f string) (string, bool) {
			switch f {
			case "Slug":
				return u.Slug, true
			case "UnitID":
				return u.UnitID.String(), true
			case "SpaceID":
				return u.SpaceID.String(), true
			}
			return "", false
		}
		if !matches(clauses, field, u.Labels, u.Annotations) {
			continue
		}
		unit := u.Unit
		ext := goclientnew.ExtendedUnit{Unit: &unit}
		if includeSpace {
			for _, sp := range s.spaces {
				if sp.SpaceID == u.SpaceID {
					ext.Space = sp
				}
			}
		}
		out = append(out, ext)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) unitByID(id string) *Unit {
	for _, u := range s.units {
		if u.UnitID.String() == id {
			return u
		}
	}
	return nil
}

func (s *Server) patchUnit(w http.ResponseWriter, unitID string, body []byte) {
	u := s.unitByID(unitID)
	if u == nil {
		writeError(w, http.StatusNotFound, "unit not found")
		return
	}
	var patch map[string]any
	if err := json.Unmarshal(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for k, v := range patch {
		switch k {
		case "HiddenReason":
			u.HiddenReason, _ = v.(string)
		default:
			writeError(w, http.StatusBadRequest, "fakehub: unsupported patch field "+k)
			return
		}
	}
	writeJSON(w, http.StatusOK, u.Unit)
}

func (s *Server) unitData(w http.ResponseWriter, unitID string) {
	u := s.unitByID(unitID)
	if u == nil {
		writeError(w, http.StatusNotFound, "unit not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, u.Data)
}

// unitSlug is the slug the simulated upload gives a file's Unit: the record's
// configName for the record, otherwise the file's base name.
func unitSlug(filePath string) string {
	if path.Base(filePath) == "installer-record.yaml" {
		return "installer"
	}
	return strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
}

func unitResource(filePath string) string {
	if path.Base(filePath) == "installer-record.yaml" {
		return "AppConfig/YAML/installer"
	}
	return "file/" + filePath
}

func (s *Server) upload(w http.ResponseWriter, q map[string]string, body []byte) {
	var req goclientnew.UploadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dryRun := req.DryRun || q["dry_run"] == "true"
	if s.refuse != nil && s.refuse(req, dryRun) {
		writeError(w, http.StatusBadRequest, "upload refused")
		return
	}
	if len(req.Components) != 1 {
		writeError(w, http.StatusBadRequest, "fakehub: exactly one component is supported")
		return
	}
	for _, dep := range req.Components[0].DependsOn {
		found := false
		for _, c := range s.components {
			found = found || c.Slug == dep
		}
		if !found {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("DependsOn names Component %q, which does not exist", dep))
			return
		}
	}
	result := s.simulateUpload(req, dryRun)
	if s.uploadHook != nil {
		result = s.uploadHook(req, dryRun, result)
	}
	status := http.StatusOK
	for _, c := range result.Components {
		for _, sp := range c.Spaces {
			for _, u := range sp.Units {
				if u.Error != nil {
					status = http.StatusMultiStatus
				}
			}
		}
	}
	writeJSON(w, status, result)
}

// simulateUpload computes, and unless dryRun applies, what an upload does:
// one Unit per file, created, updated when its content changed, or emptied
// when its file is gone.
func (s *Server) simulateUpload(req goclientnew.UploadRequest, dryRun bool) *goclientnew.UploadResult {
	comp := req.Components[0]
	space := s.spaceBySlug(comp.Space)
	spaceResult := goclientnew.UploadSpaceResult{SpaceSlug: comp.Space, Namespace: comp.Namespace, Action: "Update"}
	if space == nil {
		spaceResult.Action = "Create"
	}

	existing := map[string]*Unit{}
	if space != nil {
		for _, u := range s.units {
			if u.SpaceID == space.SpaceID && u.Labels["UploadSource"] == comp.SourceName {
				existing[u.Slug] = u
			}
		}
	}

	type write struct {
		slug, resource, data, action string
	}
	var writes []write
	seen := map[string]bool{}
	files := append([]goclientnew.UploadRequestFile(nil), req.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		slug := unitSlug(f.Path)
		seen[slug] = true
		action := "Create"
		if u, ok := existing[slug]; ok {
			action = "Unchanged"
			if u.Data != f.Content {
				action = "Update"
			}
		}
		writes = append(writes, write{slug, unitResource(f.Path), f.Content, action})
	}
	var gone []string
	for slug, u := range existing {
		if !seen[slug] && u.Data != "" {
			gone = append(gone, slug)
		}
	}
	sort.Strings(gone)
	for _, slug := range gone {
		writes = append(writes, write{slug, existing[slug].Annotations["UploadResource"], "", "Empty"})
	}

	changed := spaceResult.Action == "Create"
	for _, wr := range writes {
		changed = changed || wr.action != "Unchanged"
	}
	if !changed {
		spaceResult.Action = "Unchanged"
	}

	if !dryRun {
		if space == nil {
			space = s.addSpace(comp.Space, nil, nil)
		}
		if space.Labels == nil {
			space.Labels = map[string]string{}
		}
		for k, v := range comp.SpaceLabels {
			space.Labels[k] = v
		}
		if comp.Namespace != "" {
			space.Labels["Namespace"] = comp.Namespace
		}
		if len(comp.SpaceAnnotations) > 0 && space.Annotations == nil {
			space.Annotations = map[string]string{}
		}
		for k, v := range comp.SpaceAnnotations {
			space.Annotations[k] = v
		}
		if comp.TargetID != nil {
			space.ReleaseTargetID = comp.TargetID
		}
		found := false
		for _, c := range s.components {
			found = found || c.Slug == comp.Name
		}
		if !found {
			s.addComponent(comp.Name)
		}
		id := space.SpaceID
		spaceResult.SpaceID = &id
		if changed {
			if req.ChangeSetID != nil {
				csID := *req.ChangeSetID
				spaceResult.ChangeSetID = &csID
			} else {
				cs := s.addChangeSet(space.SpaceID, space.Slug, fmt.Sprintf("upload-%s", space.Slug))
				spaceResult.ChangeSetID = &cs.ChangeSetID
			}
		}
	}

	for _, wr := range writes {
		ur := goclientnew.UploadUnitResult{Slug: wr.slug, Action: wr.action, Resource: wr.resource}
		if !dryRun && wr.action != "Unchanged" {
			u := existing[wr.slug]
			if u == nil {
				u = &Unit{Unit: goclientnew.Unit{UnitID: s.newID(), SpaceID: space.SpaceID, SpaceSlug: space.Slug,
					Slug: wr.slug, OrganizationID: OrganizationID,
					Labels:      map[string]string{"UploadSource": comp.SourceName},
					Annotations: map[string]string{"UploadResource": wr.resource}}}
				s.units = append(s.units, u)
			}
			for k, v := range comp.UnitLabels {
				u.Labels[k] = v
			}
			for k, v := range comp.UnitAnnotations {
				u.Annotations[k] = v
			}
			u.Data = wr.data
		}
		if u := existing[wr.slug]; u != nil {
			id := u.UnitID
			ur.UnitID = &id
		}
		spaceResult.Units = append(spaceResult.Units, ur)
	}

	return &goclientnew.UploadResult{
		DryRun: dryRun,
		Components: []goclientnew.UploadComponentResult{{
			Name: comp.Name, SourceName: comp.SourceName,
			Spaces: []goclientnew.UploadSpaceResult{spaceResult},
		}},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"Code": http.StatusText(status), "Message": msg})
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
