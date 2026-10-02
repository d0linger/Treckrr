package server

import (
	"context"
	"errors"
	"strings"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// equipmentSelection is the catalog identity submitted by a booking adapter.
// A fixed rig replaces its component fields with the stored rig definition.
type equipmentSelection struct {
	GespannID   *int64
	TractorID   *int64
	LoadLevelID *int64
	MachineIDs  []int64
	TaskLabel   string
}

// equipmentResolutionFailure identifies a business-level catalog failure. The
// calling adapter translates it into its established user-facing wording.
type equipmentResolutionFailure uint8

const (
	equipmentResolutionOK equipmentResolutionFailure = iota
	equipmentResolutionIncompletePair
	equipmentResolutionEmpty
	equipmentResolutionGespannMissing
	equipmentResolutionTractorMissing
	equipmentResolutionLoadMissing
	equipmentResolutionMachineMissing
)

// resolvedEquipment contains the catalog records needed to create an immutable
// booking snapshot. It deliberately carries no neighbor, date or transaction
// choice; those remain responsibilities of the calling workflow.
type resolvedEquipment struct {
	GespannID *int64
	TaskLabel string
	Tractor   *models.Tractor
	Load      *models.LoadLevel
	Machines  []models.Machine
}

// resolveEquipment resolves a manual selection or a fixed rig without choosing
// whether it will be persisted as an Entry or a LedgerBooking. Missing catalog
// records are returned as validation failures; operational store errors remain
// errors so offline callers can retry them.
func (s *Server) resolveEquipment(ctx context.Context, selection equipmentSelection) (resolvedEquipment, equipmentResolutionFailure, error) {
	resolved := resolvedEquipment{
		GespannID: selection.GespannID,
		TaskLabel: selection.TaskLabel,
	}
	tractorID := selection.TractorID
	loadLevelID := selection.LoadLevelID
	machineIDs := append([]int64(nil), selection.MachineIDs...)

	if selection.GespannID != nil {
		gespann, err := s.store.GetGespann(ctx, *selection.GespannID)
		if errors.Is(err, store.ErrNotFound) {
			return resolved, equipmentResolutionGespannMissing, nil
		}
		if err != nil {
			return resolved, equipmentResolutionOK, err
		}
		gespannID := gespann.ID
		resolved.GespannID = &gespannID
		tractorID = gespann.TractorID
		loadLevelID = gespann.LoadLevelID
		machineIDs = append(machineIDs[:0], gespann.MachineIDs...)
		if resolved.TaskLabel == "" {
			resolved.TaskLabel = gespann.Name
		}
	}

	if (tractorID == nil) != (loadLevelID == nil) {
		return resolved, equipmentResolutionIncompletePair, nil
	}
	if tractorID == nil && len(machineIDs) == 0 {
		return resolved, equipmentResolutionEmpty, nil
	}
	if tractorID != nil {
		tractor, err := s.store.GetTractor(ctx, *tractorID)
		if errors.Is(err, store.ErrNotFound) {
			return resolved, equipmentResolutionTractorMissing, nil
		}
		if err != nil {
			return resolved, equipmentResolutionOK, err
		}
		load, err := s.store.GetLoadLevel(ctx, *loadLevelID)
		if errors.Is(err, store.ErrNotFound) {
			return resolved, equipmentResolutionLoadMissing, nil
		}
		if err != nil {
			return resolved, equipmentResolutionOK, err
		}
		resolved.Tractor = tractor
		resolved.Load = load
	}

	machines, err := s.store.MachinesByIDs(ctx, machineIDs)
	if err != nil {
		return resolved, equipmentResolutionOK, err
	}
	if len(machines) != len(machineIDs) {
		return resolved, equipmentResolutionMachineMissing, nil
	}
	resolved.Machines = machines
	return resolved, equipmentResolutionOK, nil
}

// entrySnapshot converts resolved catalog records into the immutable labels,
// identities and rate stored on a booking. The returned machine IDs follow the
// store's canonical order and are independent of the submitted slice.
func (r resolvedEquipment) entrySnapshot() (models.Entry, []int64) {
	names := make([]string, 0, len(r.Machines))
	ids := make([]int64, 0, len(r.Machines))
	for _, machine := range r.Machines {
		names = append(names, machine.Name)
		ids = append(ids, machine.ID)
	}
	entry := models.Entry{
		TaskLabel:     r.TaskLabel,
		GespannID:     r.GespannID,
		MachineLabels: strings.Join(names, ", "),
		HourlyRate:    calc.NewRateBreakdown(r.Tractor, r.Load, r.Machines).HourlyRate,
	}
	if r.Tractor != nil && r.Load != nil {
		entry.TractorID = &r.Tractor.ID
		entry.LoadLevelID = &r.Load.ID
		entry.TractorLabel = r.Tractor.Label()
		entry.LoadLabel = r.Load.Name
	}
	return entry, ids
}

// baseID returns the catalog basis represented by this resolved selection.
// Cross-basis component mixes are rejected by checkBookingCatalog afterwards.
func (r resolvedEquipment) baseID() int64 {
	if r.Tractor != nil {
		return r.Tractor.BaseID
	}
	if len(r.Machines) > 0 {
		return r.Machines[0].BaseID
	}
	return 0
}
