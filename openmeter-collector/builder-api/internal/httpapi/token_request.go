package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type tokenExchangeJSONBody struct {
	GrantType          string `json:"grant_type"`
	SubjectToken       string `json:"subject_token"`
	SubjectTokenType   string `json:"subject_token_type"`
	RequestedTokenType string `json:"requested_token_type"`
	Resource           string `json:"resource"`
	Audience           any    `json:"audience"`
	ClientID           string `json:"client_id"`
	ClientSecret       string `json:"client_secret"`
}

type subjectTokenJSONBody struct {
	SubjectToken string `json:"subject_token"`
}

// parseTokenExchangeRequest reads RFC 8693 parameters from JSON (preferred) or
// application/x-www-form-urlencoded (OAuth 2.0 default per RFC 6749 / RFC 8693).
func parseTokenExchangeRequest(r *http.Request) (tokenexchange.Request, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return tokenexchange.Request{}, fmt.Errorf("unable to read request body")
	}
	defer r.Body.Close()

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		return tokenExchangeRequestFromJSON(r, raw)
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		return tokenExchangeRequestFromForm(r, raw)
	default:
		return tokenexchange.Request{}, fmt.Errorf("content-type must be application/json or application/x-www-form-urlencoded")
	}
}

func tokenExchangeRequestFromJSON(r *http.Request, raw []byte) (tokenexchange.Request, error) {
	var body tokenExchangeJSONBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return tokenexchange.Request{}, fmt.Errorf("malformed JSON body")
	}

	clientID := strings.TrimSpace(body.ClientID)
	clientSecret := strings.TrimSpace(body.ClientSecret)
	if clientID == "" && clientSecret == "" {
		clientID, clientSecret, _ = ClientCredentialsFromRequest(r, nil)
	}

	return tokenexchange.Request{
		ClientID:           clientID,
		ClientSecret:       clientSecret,
		GrantType:          body.GrantType,
		SubjectToken:       body.SubjectToken,
		SubjectTokenType:   body.SubjectTokenType,
		RequestedTokenType: body.RequestedTokenType,
		Resource:           body.Resource,
		Audiences:          audiencesFromAny(body.Audience),
	}, nil
}

func tokenExchangeRequestFromForm(r *http.Request, raw []byte) (tokenexchange.Request, error) {
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return tokenexchange.Request{}, fmt.Errorf("malformed form body")
	}

	clientID, clientSecret, _ := ClientCredentialsFromRequest(r, form)
	return tokenexchange.Request{
		ClientID:           clientID,
		ClientSecret:       clientSecret,
		GrantType:          form.Get("grant_type"),
		SubjectToken:       form.Get("subject_token"),
		SubjectTokenType:   form.Get("subject_token_type"),
		RequestedTokenType: form.Get("requested_token_type"),
		Resource:           form.Get("resource"),
		Audiences:          form["audience"],
	}, nil
}

func audiencesFromAny(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return nonEmptyStrings(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func nonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
