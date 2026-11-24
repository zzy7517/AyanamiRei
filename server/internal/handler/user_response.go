package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	user, err := h.Queries.GetUser(r.Context(), parseUUID(requestUserID(r)))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "system user unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	writeJSON(w, http.StatusOK, h.userToResponse(user))
}

type UpdateMeRequest struct {
	Name               *string `json:"name"`
	AvatarURL          *string `json:"avatar_url"`
	Language           *string `json:"language"`
	ProfileDescription *string `json:"profile_description"`
	Timezone           *string `json:"timezone"`
}

var supportedLanguages = map[string]struct{}{
	"en":      {},
	"zh-Hans": {},
	"zh-Hant": {},
	"ko":      {},
	"ja":      {},
	"fr":      {},
	"es":      {},
	"ru":      {},
}

const MaxProfileDescriptionLen = 2000

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	var req UpdateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	currentUser, err := h.Queries.GetUser(r.Context(), parseUUID(requestUserID(r)))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	name := currentUser.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}

	params := db.UpdateUserParams{
		ID:   currentUser.ID,
		Name: name,
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, currentUser.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.Language != nil {
		lang := strings.TrimSpace(*req.Language)
		if _, ok := supportedLanguages[lang]; !ok {
			writeError(w, http.StatusBadRequest, "unsupported language")
			return
		}
		params.Language = pgtype.Text{String: lang, Valid: true}
	}
	if req.ProfileDescription != nil {
		desc := strings.TrimSpace(*req.ProfileDescription)
		if utf8.RuneCountInString(desc) > MaxProfileDescriptionLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("profile_description exceeds %d characters", MaxProfileDescriptionLen))
			return
		}
		params.ProfileDescription = pgtype.Text{String: desc, Valid: true}
	}
	if req.Timezone != nil {
		tz := strings.TrimSpace(*req.Timezone)
		if tz != "" {
			if loc, err := time.LoadLocation(tz); err != nil || loc == nil {
				writeError(w, http.StatusBadRequest, "invalid timezone")
				return
			}
		}
		params.Timezone = pgtype.Text{String: tz, Valid: true}
	}

	updatedUser, err := h.Queries.UpdateUser(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}
	writeJSON(w, http.StatusOK, h.userToResponse(updatedUser))
}

func (h *Handler) userToResponse(u db.User) UserResponse {
	questionnaire := u.OnboardingQuestionnaire
	if len(questionnaire) == 0 {
		questionnaire = []byte("{}")
	}
	return UserResponse{
		ID:                      uuidToString(u.ID),
		Name:                    u.Name,
		Email:                   u.Email,
		AvatarURL:               h.resolveAvatarURLPtr(textToPtr(u.AvatarUrl)),
		Language:                textToPtr(u.Language),
		Timezone:                textToPtr(u.Timezone),
		OnboardedAt:             timestampToPtr(u.OnboardedAt),
		OnboardingQuestionnaire: json.RawMessage(questionnaire),
		ProfileDescription:      u.ProfileDescription,
		WhatsNewSeenVersion:     textToPtr(u.WhatsNewSeenVersion),
		StarPromptShownCount:    u.StarPromptShownCount,
		StarPromptLastShownAt:   timestampToPtr(u.StarPromptLastShownAt),
		StarPromptStarredAt:     timestampToPtr(u.StarPromptStarredAt),
		CreatedAt:               timestampToString(u.CreatedAt),
		UpdatedAt:               timestampToString(u.UpdatedAt),
	}
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}
