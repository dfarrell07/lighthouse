/*
SPDX-License-Identifier: Apache-2.0

Copyright Contributors to the Submariner project.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lighthouse

import (
	"context"
	"errors"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

const PluginName = "lighthouse"

// ServeDNS implements the plugin.Handler interface.
func (lh *Lighthouse) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := &request.Request{W: w, Req: r}
	qname := state.QName()

	log.Debugf("Request received for %q", qname)

	// qname: mysvc.default.svc.example.org.
	// zone:  example.org.
	// Matches will return zone in all lower cases
	zone := plugin.Zones(lh.Zones).Matches(qname)
	if zone == "" {
		log.Debugf("Request does not match configured zones %v", lh.Zones)
		return lh.nextOrFailure(ctx, state, r, dns.RcodeNotZone)
	}

	if state.QType() != dns.TypeA && state.QType() != dns.TypeAAAA && state.QType() != dns.TypeSRV {
		log.Debugf("Query of type %d is not supported", state.QType())

		return lh.nextOrFailure(ctx, state, r, dns.RcodeNotImplemented)
	}

	zone = qname[len(qname)-len(zone):] // maintain case of original query
	state.Zone = zone

	pReq, pErr := parseRequest(state)
	if pErr != nil || pReq.podOrSvc != Svc {
		// We only support svc type queries i.e. *.svc.*
		log.Debugf("Request type %q is not a 'svc' type query - err was %v", pReq.podOrSvc, pErr)
		return lh.nextOrFailure(ctx, state, r, dns.RcodeNameError)
	}

	return lh.getDNSRecord(ctx, zone, state, w, r, pReq)
}

func (lh *Lighthouse) getDNSRecord(ctx context.Context, zone string, state *request.Request, w dns.ResponseWriter,
	r *dns.Msg, pReq *recordRequest,
) (int, error) {
	dnsRecords, isHeadless, found := lh.Resolver.GetDNSRecords(pReq.namespace, pReq.service, pReq.cluster, pReq.hostname)
	if !found {
		log.Debugf("No record found for %q", state.QName())
		return lh.nextOrFailure(ctx, state, r, dns.RcodeNameError)
	}

	if len(dnsRecords) == 0 {
		log.Debugf("Couldn't find a connected cluster or valid IPs for %q", state.QName())
		return lh.emptyResponse(state)
	}

	if state.QType() == dns.TypeAAAA {
		log.Debugf("Returning empty response for TypeAAAA query")
		return lh.emptyResponse(state)
	}

	// Count records
	localClusterID := lh.ClusterStatus.GetLocalClusterID()
	for _, record := range dnsRecords {
		incDNSQueryCounter(localClusterID, record.ClusterName, pReq.service, pReq.namespace, record.IP)
	}

	records := make([]dns.RR, 0)

	if state.QType() == dns.TypeA {
		records = lh.createARecords(dnsRecords, state)
	} else if state.QType() == dns.TypeSRV {
		records = lh.createSRVRecords(dnsRecords, state, pReq, zone, isHeadless)
	}

	if len(records) == 0 {
		log.Debugf("Couldn't find a connected cluster or valid record for %q", state.QName())
		return lh.emptyResponse(state)
	}

	log.Debugf("rr is %v", records)

	a := new(dns.Msg)
	a.SetReply(r)
	a.Authoritative = true
	a.Answer = append(a.Answer, records...)
	log.Debugf("Responding to query with '%s'", a.Answer)

	wErr := w.WriteMsg(a)
	if wErr != nil {
		// Error writing reply msg
		log.Errorf("Failed to write message %#v: %v", a, wErr)
		return dns.RcodeServerFailure, lh.error("failed to write response")
	}

	return dns.RcodeSuccess, nil
}

func (lh *Lighthouse) emptyResponse(state *request.Request) (int, error) {
	a := new(dns.Msg)
	a.SetReply(state.Req)

	return lh.writeResponse(state, a)
}

func (lh *Lighthouse) writeResponse(state *request.Request, a *dns.Msg) (int, error) {
	a.Authoritative = true

	wErr := state.W.WriteMsg(a)
	if wErr != nil {
		log.Errorf("Failed to write message %#v: %v", a, wErr)
		return dns.RcodeServerFailure, lh.error("failed to write response")
	}

	return dns.RcodeSuccess, nil
}

// Name implements the Handler interface.
func (lh *Lighthouse) Name() string {
	return PluginName
}

func (lh *Lighthouse) error(str string) error {
	return plugin.Error(lh.Name(), errors.New(str)) //nolint:wrapcheck // Let the caller wrap it.
}

func (lh *Lighthouse) nextOrFailure(ctx context.Context, state *request.Request, r *dns.Msg, rcode int) (int, error) {
	if lh.Fall.Through(state.Name()) {
		return plugin.NextOrFailure(lh.Name(), lh.Next, ctx, state.W, r) //nolint:wrapcheck // Let the caller wrap it.
	}

	a := new(dns.Msg)
	a.SetRcode(r, rcode)

	return lh.writeResponse(state, a)
}
