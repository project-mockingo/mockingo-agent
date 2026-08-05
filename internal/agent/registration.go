package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Registration struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	PublicURL    string `json:"publicUrl"`
	ConnectURL   string `json:"connectUrl"`
	SessionToken string `json:"sessionToken"`
}

type registrationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Register(ctx context.Context, client *http.Client, apiURL, token, name string, port int) (Registration, error) {
	payload, _ := json.Marshal(map[string]any{"name": name, "protocol": "http", "localPort": port})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiURL, "/")+"/api/v1/tunnels", bytes.NewReader(payload))
	if err != nil {
		return Registration{}, fmt.Errorf("create registration request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Registration{}, fmt.Errorf("contact gateway: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		var apiErr registrationError
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiErr)
		if response.StatusCode == http.StatusUnauthorized {
			return Registration{}, fmt.Errorf("authentication rejected by gateway")
		}
		if apiErr.Message == "" {
			apiErr.Message = response.Status
		}
		return Registration{}, fmt.Errorf("gateway rejected registration: %s", apiErr.Message)
	}
	var registration Registration
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&registration); err != nil {
		return Registration{}, fmt.Errorf("decode gateway registration: %w", err)
	}
	if registration.ID == "" || registration.ConnectURL == "" || registration.SessionToken == "" {
		return Registration{}, fmt.Errorf("gateway returned an incomplete registration")
	}
	return registration, nil
}

func Delete(ctx context.Context, client *http.Client, apiURL, token, id string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(apiURL, "/")+"/api/v1/tunnels/"+id, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("gateway returned %s", response.Status)
	}
	return nil
}
