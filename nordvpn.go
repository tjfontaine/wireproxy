package wireproxy

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

type NordvpnServer struct {
	Name     string `json:"name"`
	Station  string `json:"station"`
	Hostname string `json:"hostname"`
}

const NORD_URL = "https://api.nordvpn.com/v1/servers/recommendations?limit=3"

func GetNordvpnServers() ([]NordvpnServer, error) {
	client := http.Client{}
	req, err := http.NewRequest(http.MethodGet, NORD_URL, nil)

	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("broken error code: %+v", res)
	}

	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		return nil, err
	}

	var servers []NordvpnServer

	err = json.Unmarshal(body, &servers)

	if err != nil {
		return nil, err
	}

	return servers, nil
}
