package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

func (s *Server) createDeletePlan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		InstanceIDs []string `json:"instance_ids"`
	}
	if err := decodeBody(w, r, &request); err != nil || len(request.InstanceIDs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "至少选择一个种子任务")
		return
	}
	saved, err := s.deletions.CreatePlan(r.Context(), request.InstanceIDs)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, saved.Plan)
}

func (s *Server) createDeleteJob(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeBody(w, r, &request); err != nil || request.PlanID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "必须提供删除计划 ID")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeAPIError(w, http.StatusBadRequest, "missing_idempotency_key", "必须提供有效的 Idempotency-Key")
		return
	}
	job, err := s.deletions.CreateJob(r.Context(), request.PlanID, idempotencyKey)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, jobResponse(job))
}

func (s *Server) getDeleteJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.deletions.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, jobResponse(job))
}

func jobResponse(job store.DeleteJob) map[string]any {
	return map[string]any{
		"id": job.ID, "plan_id": job.PlanID, "status": normalizeJobStatus(job.Status),
		"internal_status": job.Status, "error": job.Error,
		"steps": job.Steps, "created_at": job.CreatedAt, "updated_at": job.UpdatedAt,
	}
}
