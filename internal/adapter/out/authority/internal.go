package authority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/correlation"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// errOwnerNotFound is the owner's 404 for the requested resource.
var errOwnerNotFound = errors.New("owner resource not found")

// Client calls an owner's private /internal/v1 contract with the service
// credential and the request's correlation id. HTTP.Timeout bounds each call;
// anything but a well-formed 200 or 404 is out.ErrUnavailable (fail closed).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c *Client) get(ctx context.Context, path string, query url.Values, dst any) error {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return c.do(ctx, http.MethodGet, path, "", nil, dst)
}

// post sends a coordination command; idempotencyKey lets the owner deduplicate
// retries (Norma 5.3.8).
func (c *Client) post(ctx context.Context, path, idempotencyKey string, body, dst any) error {
	return c.do(ctx, http.MethodPost, path, idempotencyKey, body, dst)
}

func (c *Client) do(ctx context.Context, method, path, idempotencyKey string, body, dst any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%v: %w", err, out.ErrUnavailable)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/internal/v1"+path, payload)
	if err != nil {
		return fmt.Errorf("%v: %w", err, out.ErrUnavailable)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if id := correlation.ID(ctx); id != "" {
		req.Header.Set("X-Correlation-Id", id)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("owner call: %v: %w", err, out.ErrUnavailable)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		dec := json.NewDecoder(res.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			return fmt.Errorf("owner response: %v: %w", err, out.ErrUnavailable)
		}
		return nil
	case http.StatusNotFound:
		return errOwnerNotFound
	case http.StatusConflict:
		return out.ErrOwnerConflict
	default:
		return fmt.Errorf("owner answered %d: %w", res.StatusCode, out.ErrUnavailable)
	}
}

// Patients adapts GET /internal/v1/patients/{id}/validation (patients-service,
// scope patients:validate) to out.PatientDirectory.
type Patients struct{ Client *Client }

var _ out.PatientDirectory = Patients{}

func (p Patients) IsActive(ctx context.Context, patientID string) (bool, error) {
	var v struct {
		PatientID string `json:"patientId"`
		Active    *bool  `json:"active"`
		Version   int    `json:"version"`
	}
	err := p.Client.get(ctx, "/patients/"+url.PathEscape(patientID)+"/validation", nil, &v)
	if errors.Is(err, errOwnerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if v.PatientID != patientID || v.Active == nil {
		return false, fmt.Errorf("patient validation does not describe the requested patient: %w", out.ErrUnavailable)
	}
	return *v.Active, nil
}

// Appointments adapts GET /internal/v1/patients/{id}/assignments (appointments-
// service, scope assignments:read) to out.CareAuthority.
type Appointments struct{ Client *Client }

var _ out.CareAuthority = Appointments{}

func (a Appointments) IsAssigned(ctx context.Context, patientID, dentistID string) (bool, error) {
	var v struct {
		PatientID           string   `json:"patientId"`
		DentistID           string   `json:"dentistId"`
		Assigned            *bool    `json:"assigned"`
		RelationshipVersion int      `json:"relationshipVersion"`
		SourceTypes         []string `json:"sourceTypes"`
	}
	err := a.Client.get(ctx, "/patients/"+url.PathEscape(patientID)+"/assignments", url.Values{"dentistId": {dentistID}}, &v)
	if errors.Is(err, errOwnerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if v.PatientID != patientID || v.DentistID != dentistID || v.Assigned == nil {
		return false, fmt.Errorf("assignment validation does not describe the requested pair: %w", out.ErrUnavailable)
	}
	return *v.Assigned, nil
}

// maxAssignedPages bounds the paging of one dentist's assignments
// (100 per page); a larger answer fails closed instead of looping.
const maxAssignedPages = 50

// AssignedPatients pages through GET /internal/v1/dentists/{id}/assigned-patients.
// Every page must belong to the same assignmentsVersion snapshot; an unknown
// dentist has no patients.
func (a Appointments) AssignedPatients(ctx context.Context, dentistID string) ([]string, error) {
	ids := []string{}
	version := 0
	for page := 1; ; page++ {
		var v assignedPatientPage
		err := a.Client.get(ctx, "/dentists/"+url.PathEscape(dentistID)+"/assigned-patients",
			url.Values{"page": {strconv.Itoa(page)}, "limit": {"100"}}, &v)
		if errors.Is(err, errOwnerNotFound) {
			return []string{}, nil
		}
		if err != nil {
			return nil, err
		}
		switch {
		case v.DentistID != dentistID:
			return nil, fmt.Errorf("assigned patients of another dentist: %w", out.ErrUnavailable)
		case version != 0 && v.AssignmentsVersion != version:
			return nil, fmt.Errorf("assignments changed while paging: %w", out.ErrUnavailable)
		case v.Meta.TotalPages > maxAssignedPages:
			return nil, fmt.Errorf("more than %d pages of assigned patients: %w", maxAssignedPages, out.ErrUnavailable)
		}
		version = v.AssignmentsVersion
		for _, p := range v.Data {
			ids = append(ids, p.PatientID)
		}
		if page >= v.Meta.TotalPages {
			return ids, nil
		}
	}
}

type assignedPatientPage struct {
	DentistID          string `json:"dentistId"`
	AssignmentsVersion int    `json:"assignmentsVersion"`
	Data               []struct {
		PatientID           string   `json:"patientId"`
		RelationshipVersion int      `json:"relationshipVersion"`
		SourceTypes         []string `json:"sourceTypes"`
	} `json:"data"`
	Meta struct {
		Page       int `json:"page"`
		Limit      int `json:"limit"`
		Total      int `json:"total"`
		TotalPages int `json:"totalPages"`
	} `json:"meta"`
}

// Sessions adapts POST /internal/v1/sessions/validations (auth-service, scope
// identity:validate): auth decides revocation and account state. The token is
// sent in the body only and never logged.
type Sessions struct{ Client *Client }

func (s Sessions) IsActive(ctx context.Context, accessToken string) (bool, error) {
	var v struct {
		Active *bool `json:"active"`
	}
	err := s.Client.post(ctx, "/sessions/validations", "", map[string]string{"accessToken": accessToken}, &v)
	if errors.Is(err, errOwnerNotFound) {
		return false, fmt.Errorf("session validation not found: %w", out.ErrUnavailable)
	}
	if err != nil {
		return false, err
	}
	if v.Active == nil {
		return false, fmt.Errorf("session validation without active: %w", out.ErrUnavailable)
	}
	return *v.Active, nil
}
