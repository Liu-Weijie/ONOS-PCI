// SPDX-FileCopyrightText: 2022-present Intel Corporation
// SPDX-FileCopyrightText: 2020-present Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	e2smrccomm "github.com/onosproject/onos-e2-sm/servicemodels/e2sm_rc/v1/e2sm-common-ies"
	"github.com/onosproject/onos-lib-go/pkg/errors"
	"github.com/onosproject/onos-lib-go/pkg/logging"
	"github.com/onosproject/onos-pci/pkg/store/metrics"
	"github.com/onosproject/onos-pci/pkg/types"
	"github.com/onosproject/onos-pci/pkg/utils/decode"
	"github.com/onosproject/onos-pci/pkg/utils/parse"
)

// SearchDepth indicates how deep it will search in metrics store
// neighbor only = 1; neighbor and neighbor's neighbor = 2
const SearchDepth = 2

var log = logging.GetLogger()

func NewPciController(store metrics.Store) PciController {
	return PciController{
		metricStore: store,
	}
}

type PciController struct {
	metricStore metrics.Store
}

func (p *PciController) Run(ctx context.Context) {
	go p.resolvePciConflict(ctx)
}

func (p *PciController) resolvePciConflict(ctx context.Context) {
	ch := make(chan metrics.Event)
	err := p.metricStore.Watch(ctx, ch)
	if err != nil {
		log.Error(err)
	}
	for e := range ch {
		// new indication message arrives
		if e.Type == metrics.Created {
			log.Debugf("new event indication message key: %v / value: %v / event type: %v",
				e.Key, e.Value, e.Type)

			pci, changed, err := p.getAvailablePci(ctx, &e.Value)
			if err != nil {
				log.Errorf("skip pci logic for event %v due to %v", e, err)
				continue
			}

			if changed {
				log.Debugf("NewPCI for %v: %v", e.Value.Key, pci)
				err := p.metricStore.UpdatePci(ctx, metrics.NewKey(e.Value.Key.CellGlobalID), pci)
				if err != nil {
					log.Error(err)
				}
			}
		}
	}
}

func (p *PciController) getAvailablePci(ctx context.Context, entry *metrics.Entry) (int32, bool, error) {
	pciMap, err := p.getEmptyPciMap(entry.Value.PCIPoolList)
	if err != nil {
		return 0, false, err
	}

	// Make a PCI map to check which PCIs in the PciPool are occupied
	err = p.neighborTraversal(ctx, entry, entry, 1, pciMap)
	if err != nil {
		return 0, false, err
	}

	// if the PCI that entry has is not occupied by the other cells in the scope (depth), just use it
	if !pciMap[entry.Value.Metric.PCI] {
		return 0, false, nil
	}

	// Pick one of PCIs in map, if the selected PCI is not occupied
	for k, v := range pciMap {
		if !v {
			return k, true, nil
		}
	}

	// if all PCIs are occupied by the other cells in the scope (depth), rise error and return the same PCI
	return 0, false, errors.NewUnavailable("All PCIs in the PciPool are occupied by the other cells in the scope")
}

func (p *PciController) getEmptyPciMap(pciPoolList []*types.PCIPool) (map[int32]bool, error) {
	pciMap := make(map[int32]bool)
	for _, pciPool := range pciPoolList {
		if pciPool.LowerPci > pciPool.UpperPci {
			return nil, errors.NewUnavailable("lower pci should be lower than upper pci")
		}
		for i := pciPool.LowerPci; i <= pciPool.UpperPci; i++ {
			pciMap[i] = false
		}
	}
	return pciMap, nil
}

func (p *PciController) neighborTraversal(ctx context.Context, root *metrics.Entry, entry *metrics.Entry, cDepth int, pciMap map[int32]bool) error {
	var err error
	if cDepth > SearchDepth {
		// if this is the leaf entry, then return
		return err
	}

	rootArfcn := root.Value.Metric.ARFCN

	for _, n := range entry.Value.Neighbors {
		neighborCGI := &e2smrccomm.Cgi{}
		var pci int32
		var arfcn int32
		if n.GetRanTypeChoiceNr() != nil {
			neighborCGI.Cgi = &e2smrccomm.Cgi_NRCgi{
				NRCgi: n.GetRanTypeChoiceNr().GetNRCgi(),
			}
			pci = n.GetRanTypeChoiceNr().GetNRPci().GetValue()
			arfcn = n.GetRanTypeChoiceNr().GetNRFreqInfo().GetNrArfcn().GetNRarfcn()
		} else if n.GetRanTypeChoiceEutra() != nil {
			neighborCGI.Cgi = &e2smrccomm.Cgi_EUtraCgi{
				EUtraCgi: n.GetRanTypeChoiceEutra().GetEUtraCgi(),
			}
			pci = n.GetRanTypeChoiceEutra().GetEUtraPci().GetValue()
			arfcn = n.GetRanTypeChoiceEutra().GetEUtraArfcn().GetValue()
		} else {
			log.Errorf("Neighbor type should be NR or EUTRAN: %v", n)
			continue
		}

		// is CGI root key equal to neighbor CGI? - if so, skip; otherwise, mark pciMap as false
		if !p.isCGIEqual(root.Key.CellGlobalID, neighborCGI) {
			neighborEntry := p.getEntryWithNeighborCGI(ctx, neighborCGI)
			if neighborEntry != nil {
				// if neighbor metric is in store - search store first:
				// neighbor metric has more recent PCI than the neighbors field in entry,
				// because this controller updates PCI in neighbor metric after sending RC-PRE control message
				if rootArfcn == arfcn {
					pciMap[neighborEntry.Value.Metric.PCI] = true
				}
				err = p.neighborTraversal(ctx, root, neighborEntry, cDepth+1, pciMap)
				if err != nil {
					log.Error(err)
				}
			} else {
				// if neighbor metric is not in store, but in the entry neighbors field
				// hit here in the case when ind message was not arrived yet or the neighbor is not connected to the E2Nodes subscribing with this app
				if rootArfcn == arfcn {
					pciMap[pci] = true
				}
			}
		}
	}

	return err
}

// getEntryWithNeighborCGI gets entry in store with CGI value, not entry key (not pointer)
// used when searching neighbor entry in store
// since entry key is the pointer of CGI, it is impossible to get entry with CGI in neighbor field
func (p *PciController) getEntryWithNeighborCGI(ctx context.Context, id *e2smrccomm.Cgi) *metrics.Entry {
	ch := make(chan *metrics.Entry)
	var targetEntry *metrics.Entry
	go func(chan *metrics.Entry) {
		err := p.metricStore.Entries(ctx, ch)
		if err != nil {
			log.Error(err)
		}
	}(ch)
	for entry := range ch {
		if p.isCGIEqual(id, entry.Key.CellGlobalID) {
			targetEntry = entry
		}
	}
	return targetEntry
}

// isCGIEqual compares CGI values, not pointers
func (p *PciController) isCGIEqual(s *e2smrccomm.Cgi, t *e2smrccomm.Cgi) bool {
	// both s and t should be either 4G or 5G
	if s.GetNRCgi() != nil && t.GetNRCgi() != nil {
		sPlmnID, sCellID, sCGIType, err := parse.GetNRMetricKey(s.GetNRCgi())
		if err != nil {
			log.Errorf("could not parse source CGI: %v", err)
			return false
		}
		tPlmnID, tCellID, tCGIType, err := parse.GetNRMetricKey(t.GetNRCgi())
		if err != nil {
			log.Errorf("could not parse target CGI: %v", err)
			return false
		}
		if decode.PlmnIDToUint32(sPlmnID) == decode.PlmnIDToUint32(tPlmnID) &&
			sCellID == tCellID && sCGIType == tCGIType {
			return true
		}
	} else if s.GetEUtraCgi() != nil && t.GetEUtraCgi() != nil {
		sPlmnID, sCellID, sCGIType, err := parse.GetEUTRAMetricKey(s.GetEUtraCgi())
		if err != nil {
			log.Errorf("could not parse source CGI: %v", err)
			return false
		}
		tPlmnID, tCellID, tCGIType, err := parse.GetEUTRAMetricKey(t.GetEUtraCgi())
		if err != nil {
			log.Errorf("could not parse target CGI: %v", err)
			return false
		}
		if decode.PlmnIDToUint32(sPlmnID) == decode.PlmnIDToUint32(tPlmnID) &&
			sCellID == tCellID && sCGIType == tCGIType {
			return true
		}
	}

	return false
}
