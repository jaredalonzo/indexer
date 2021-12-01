package fetcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"go.uber.org/zap"
)

const ConnectionApiCount = 2

func (f *fetcher) FetchConnections(address string) (results []ConnectionEntry, err error) {
	ch := make(chan ConnectionEntryList)

	// Part 1 - Demo data source
	// Context API
	go f.processContextConn(address, ch)
	// Rarible API
	go f.processRaribleConn(address, ch)
	// Part 2 - Add other data source here
	// TODO

	/* bounty: https://gitcoin.co/issue/cyberconnecthq/indexer/2/100027191 test sample
	// this is a simple test for printing out all the addresses of token holders
	// from the getPoaprecommendation func
	// you can uncomment to verify the results are correct

	test := f.getPoapRecommendation(address)

	for _, i := range test {
		fmt.Println(i.EventID, i.Address)
	}
	*/

	// Final Part - Aggregate all data & convert ens domain & filter out invalid connections
	for i := 0; i < ConnectionApiCount; i++ {
		entry := <-ch
		if entry.Err != nil {
			zap.L().With(zap.Error(entry.Err)).Error("connection api error: " + entry.msg)
			continue
		}
		results = append(results, entry.Conn...)
	}

	return
}

func (f *fetcher) getRaribleConnection(address string, isFollowing bool) ([]RaribleConnectionResp, error) {
	// Prepare request
	var url string
	if isFollowing {
		url = fmt.Sprintf(RaribleFollowingUrl, address)
	} else {
		url = fmt.Sprintf(RaribleFollowerUrl, address)
	}

	postBody, _ := json.Marshal(map[string]int{
		"size": 5000, // TODO
	})

	body, err := sendRequest(f.httpClient, RequestArgs{
		url:    url,
		method: "POST",
		body:   postBody,
	})

	var results []RaribleConnectionResp
	err = json.Unmarshal(body, &results)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (f *fetcher) processRaribleConn(address string, ch chan<- ConnectionEntryList) {
	var rarTotal []RaribleConnectionResp
	result := ConnectionEntryList{}

	// Query Followings from Rarible
	rarFollowings, err := f.getRaribleConnection(address, true)
	if err != nil {
		result.Err = err
		result.msg = "[processRaribleConn] fetch Rarible followings failed"
		ch <- result
		return
	}

	// Query Followers from Rarible
	rarFollowers, err := f.getRaribleConnection(address, false)
	if err != nil {
		result.Err = err
		result.msg = "[processRaribleConn] fetch Rarible followers failed"
		ch <- result
		return
	}

	// Merge and printing out for Rarible followings
	rarTotal = append(rarFollowers, rarFollowings...)
	var results []ConnectionEntry
	for i := 0; i < len(rarTotal); i++ {
		if !addressFilter(rarTotal[i].Following.From) || !addressFilter(rarTotal[i].Following.To) {
			continue
		}
		result := ConnectionEntry{
			From:     rarTotal[i].Following.From,
			To:       rarTotal[i].Following.To,
			Platform: RARIBLE,
		}
		results = append(results, result)
	}

	result.Conn = append(result.Conn, results...)
	ch <- result
}

func (f *fetcher) getUserContextConnection(address string, isFollowing bool) (results []ConnectionEntry, err error) {
	var url string

	if isFollowing {
		url = fmt.Sprintf(ContextUrl, address+"/following")
	} else {
		url = fmt.Sprintf(ContextUrl, address+"/followers")
	}

	body, err := sendRequest(f.httpClient, RequestArgs{
		url:    url,
		method: "GET",
	})
	if err != nil {
		return nil, err
	}

	var contextRecord ContextConnection
	err = json.Unmarshal(body, &contextRecord)
	if err != nil {
		return nil, err
	}

	if isFollowing {
		for i := 0; i < len(contextRecord.Relationships); i++ {
			var toAddr string
			toActor := contextRecord.Relationships[i].Actor
			if isAddress(toActor) {
				toAddr = toActor
			} else if len(contextRecord.Profiles[toActor]) != 0 {
				toAddr = contextRecord.Profiles[toActor][0].Address
			} else {
				// Context.app lacks of data
				continue
			}
			if !addressFilter(toAddr) {
				continue
			}
			newContextRecord := ConnectionEntry{
				From:     address,
				To:       toAddr,
				Platform: CONTEXT,
			}
			results = append(results, newContextRecord)
		}
	} else {
		for i := 0; i < len(contextRecord.Relationships); i++ {
			var fromAddr string
			profileAcct := contextRecord.Relationships[i].Actor
			if len(contextRecord.Profiles[profileAcct]) != 0 {
				fromAddr = contextRecord.Profiles[profileAcct][0].Address
			} else {
				// Context.app lacks of data
				continue
			}
			if !addressFilter(fromAddr) {
				continue
			}
			newContextRecord := ConnectionEntry{
				From:     fromAddr,
				To:       address,
				Platform: CONTEXT,
			}
			results = append(results, newContextRecord)
		}
	}
	return results, nil
}

func (f *fetcher) processContextConn(address string, ch chan<- ConnectionEntryList) {
	result := ConnectionEntryList{}
	followingResults, err := f.getUserContextConnection(address, true)
	if err != nil {
		result.Err = err
		result.msg = "[processContextConn] fetch Context followings failed"
		ch <- result
		return
	}

	followerResults, err := f.getUserContextConnection(address, false)
	if err != nil {
		result.Err = err
		result.msg = "[processContextConn] fetch Context followers failed"
		ch <- result
		return
	}

	followingResults = append(followingResults, followerResults...)
	result.Conn = append(result.Conn, followingResults...)
	ch <- result
}

// return false if input is neither Ethereum address nor ENS
func addressFilter(addr string) bool {
	if isAddress(addr) {
		return true
	} else if len(addr) > 4 && addr[len(addr)-4:] == ".eth" {
		return true
	} else {
		return false
	}
}

// This func will get all the poap events of the given address
func (f *fetcher) processPoap(address string) []UserPoapIdentity {
	var result []UserPoapIdentity

	body, err := sendRequest(f.httpClient, RequestArgs{
		url:    fmt.Sprintf(PoapUrl, address),
		method: "GET",
	})
	if err != nil {
		zap.L().With(zap.Error(err)).Error("[processPoap] request poap api error")
		return nil
	}
	poapProfiles := PoapApiResp{}
	err = json.Unmarshal(body, &poapProfiles)
	if err != nil {
		zap.L().With(zap.Error(err)).Error("[processPoap] poap api unmarshal failed")
		return nil
	}

	for _, poapProfile := range poapProfiles {
		var poapEvent UserPoapIdentity

		poapEvent.EventID = strconv.Itoa(poapProfile.Event.ID)
		poapEvent.EventDesc = poapProfile.Event.Description
		poapEvent.TokenID = poapProfile.TokenID
		poapEvent.EventName = poapProfile.Event.EventName
		poapEvent.EventUrl = poapProfile.Event.EventUrl
		result = append(result, poapEvent)
	}

	return result
}

// This func will process all the events from processPoap and get all the
// recommendations for addresses that have redeemed their POAP NFTs from the same
// event.

// Some possible improvements: improve the nested for loop structure since each event
// will query the POAP graph to get an array of results for one specific event ID
func (f *fetcher) getPoapRecommendation(address string) []PoapRecommendation {
	var result []PoapRecommendation

	poapEvents := f.processPoap(address)
	for _, event := range poapEvents {
		id := event.EventID
		poapQuery := map[string]string{
			"query": fmt.Sprintf(`
				{
					event(id: "%s") {
						tokens {
							id
							owner {
								id
							}
						}
					}
				}
			 `, id),
		}
		poapBody, _ := json.Marshal(poapQuery)
		body, err := sendRequest(f.httpClient, RequestArgs{
			url:    fmt.Sprintf(PoapSubgraphUrl),
			method: "POST",
			body:   bytes.NewBuffer(poapBody).Bytes(),
		})
		if err != nil {
			zap.L().With(zap.Error(err)).Error("[getPoapRecommendation] request subgraph api error")
			return nil // do we want to return nil if one of the graph requests fail?
		}
		poapGraph := PoapGraphResp{}
		err = json.Unmarshal(body, &poapGraph)
		if err != nil {
			zap.L().With(zap.Error(err)).Error("[getPoapRecommendation] POAP subgraph unmarshal error")
			return nil // do we want to return nil if one of the graph requests fail?
		}

		for _, token := range poapGraph.Data.Event.Tokens {
			var poapRec PoapRecommendation

			poapRec.Address = token.Owner.ID
			poapRec.TokenID = token.ID
			poapRec.EventID = id
			result = append(result, poapRec)
		}
	}
	return result
}
