package model

import "time"

// ClinicalRecord is the single longitudinal record of a patient. The patient
// is a contractual reference to Patients; no administrative profile lives here.
type ClinicalRecord struct {
	ID        string
	PatientID string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int
}

// OpenRecord creates the record. Uniqueness per patient is enforced by the
// repository; author and time always come from the server.
func OpenRecord(id, patientID, actorID string, at time.Time) (ClinicalRecord, error) {
	switch {
	case patientID == "":
		return ClinicalRecord{}, invalid("patientId", "required")
	case actorID == "":
		return ClinicalRecord{}, invalid("authorId", "required")
	}
	return ClinicalRecord{ID: id, PatientID: patientID, CreatedBy: actorID, CreatedAt: at, UpdatedAt: at, Version: 1}, nil
}
