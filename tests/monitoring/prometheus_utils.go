package monitoring

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/gomega"

	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/kubevirt/tests"
)

type AlertRequestResult struct {
	Alerts prometheusv1.AlertsResult `json:"data"`
	Status string                    `json:"status"`
}

func getAlerts(cli kubecli.KubevirtClient) ([]prometheusv1.Alert, error) {
	bodyBytes := DoPrometheusHTTPRequest(cli, "/alerts")

	var result AlertRequestResult
	err := json.Unmarshal(bodyBytes, &result)
	if err != nil {
		return nil, err
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("api request failed. result: %v", result)
	}

	return result.Alerts.Alerts, nil
}

func DoPrometheusHTTPRequest(cli kubecli.KubevirtClient, endpoint string) []byte {

	monitoringNs := getMonitoringNs(cli)
	token := getAuthorizationToken(cli, monitoringNs)

	var result []byte
	var err error
	if tests.IsOpenShift() {
		url := getPrometheusURLForOpenShift()
		resp := doHttpRequest(url, endpoint, token)
		defer resp.Body.Close()
		result, err = ioutil.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
	} else {
		sourcePort := 4321 + rand.Intn(6000)
		targetPort := 9090
		Eventually(func() error {
			_, cmd, err := tests.CreateCommandWithNS(monitoringNs, tests.GetK8sCmdClient(),
				"port-forward", "service/prometheus-k8s", fmt.Sprintf("%d:%d", sourcePort, targetPort))
			if err != nil {
				return err
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			waitForPortForwardCmd(stdout, sourcePort, targetPort)
			defer killPortForwardCommand(cmd)

			url := fmt.Sprintf("http://localhost:%d", sourcePort)
			resp := doHttpRequest(url, endpoint, token)
			defer resp.Body.Close()
			result, err = ioutil.ReadAll(resp.Body)
			return err
		}, 10*time.Second, time.Second).ShouldNot(HaveOccurred())
	}
	return result
}

func getPrometheusURLForOpenShift() string {
	var host string

	Eventually(func() error {
		var stderr string
		var err error
		host, stderr, err = tests.RunCommand(tests.GetK8sCmdClient(), "-n", "openshift-monitoring", "get", "route", "prometheus-k8s", "--template", "{{.spec.host}}")
		if err != nil {
			return fmt.Errorf("error while getting route. err:'%v', stderr:'%v'", err, stderr)
		}
		return nil
	}, 10*time.Second, time.Second).Should(BeTrue())

	return fmt.Sprintf("https://%s", host)
}

func doHttpRequest(url string, endpoint string, token string) *http.Response {
	var resp *http.Response
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	Eventually(func() bool {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/%s", url, endpoint), nil)
		if err != nil {
			return false
		}
		req.Header.Add("Authorization", "Bearer "+token)
		resp, err = client.Do(req)
		if err != nil {
			return false
		}
		if resp.StatusCode != http.StatusOK {
			return false
		}
		return true
	}, 10*time.Second, 1*time.Second).Should(BeTrue())

	return resp
}

func getAuthorizationToken(cli kubecli.KubevirtClient, monitoringNs string) string {
	var token string
	Eventually(func() bool {
		var secretName string
		sa, err := cli.CoreV1().ServiceAccounts(monitoringNs).Get(context.TODO(), "prometheus-k8s", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, secret := range sa.Secrets {
			if strings.HasPrefix(secret.Name, "prometheus-k8s-token") {
				secretName = secret.Name
			}
		}
		secret, err := cli.CoreV1().Secrets(monitoringNs).Get(context.TODO(), secretName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		if _, ok := secret.Data["token"]; !ok {
			return false
		}
		token = string(secret.Data["token"])
		return true
	}, 10*time.Second, time.Second).Should(BeTrue())
	return token
}

func getMonitoringNs(cli kubecli.KubevirtClient) string {
	if tests.IsOpenShift() {
		return "openshift-monitoring"
	}

	return "monitoring"
}

func waitForPortForwardCmd(stdout io.ReadCloser, src, dst int) {
	Eventually(func() string {
		tmp := make([]byte, 1024)
		_, err := stdout.Read(tmp)
		Expect(err).NotTo(HaveOccurred())

		return string(tmp)
	}, 30*time.Second, 1*time.Second).Should(ContainSubstring(fmt.Sprintf("Forwarding from 127.0.0.1:%d -> %d", src, dst)))
}

func killPortForwardCommand(portForwardCmd *exec.Cmd) error {
	if portForwardCmd == nil {
		return nil
	}

	portForwardCmd.Process.Kill()
	_, err := portForwardCmd.Process.Wait()
	return err
}
