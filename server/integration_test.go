package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testId string
var imageName string
var testNodePort string
var serviceIPaddress string
var serviceName string = "auto-kubernetes-lets-encrypt"
var failed bool = false
var ZONE_ID string = "2fcce5055b9bdafff28874ed2f5a4140"
var dnsRecordId string
var DOMAIN string = "jorge.fail"
var CLOUDFLARE_EMAIL string = "jorge.silva@thejsj.com"
var CLOUDFLARE_API_KEY string = os.Getenv("CLOUDFLARE_API_KEY")
var JOB_NAME string = "auto-kubernetes-lets-encrypt"
var CERT_SECRET_NAME string = "auto-kubernetes-lets-encrypt-certs"
var REGISTRATION_SECRET_NAME string = "auto-kubernetes-lets-encrypt-user"

type K8sJobResponse struct {
	Status K8sJobStatusResponse `json:"status"`
}
type K8sJobStatusResponse struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}
type K8sServiceResponse struct {
	Status K8sServiceStatusResponse `json:"status"`
}
type K8sSecretResponse struct {
	Data map[string]string `json:"data"`
}
type K8sServiceStatusResponse struct {
	LoadBalancer K8sLoadBalancerResponse `json:"loadBalancer"`
}
type K8sLoadBalancerResponse struct {
	Ingress []K8sIngressEntryResponse `json:"ingress"`
}
type K8sIngressEntryResponse struct {
	Ip string `json:"ip"`
}
type CloudflareZoneCreationResponse struct {
	Result CloudflareZoneCreationResult `json:"result"`
}
type CloudflareZoneCreationResult struct {
	Id string `json:"id"`
}

// #1 It should build an image
func TestBuildingImage(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Start build. Check for commit ENV")
	commit := os.Getenv("BUILD_GIT_COMMIT")
	if len(commit) == 0 {
		t.Fatal("No ENV passed for `BUILD_GIT_COMMIT`. Cannot build image")
	}
	imageName = fmt.Sprintf("quay.io/hiphipjorge/auto-kubernetes-lets-encrypt:%s", commit)
	if os.Getenv("SKIP_BUILD") != "" {
		return
	}
	fullCommand := fmt.Sprintf("docker build -t %s ../server/", imageName)
	t.Logf("Build with command: `%s`", fullCommand)
	err, output := execCommand(fullCommand)
	if err != nil {
		failed = true
		t.Fatalf("Error building image: %s", output)
	}
}

// #2 It should push the image
func TestPushingImage(t *testing.T) {
	if failed || os.Getenv("SKIP_BUILD") != "" {
		t.SkipNow()
	}

	t.Log("Start push. Check for commit ENV")
	fullCommand := fmt.Sprintf("docker push %s", imageName)
	t.Logf("Push with command: `%s`", fullCommand)
	err, output := execCommand(fullCommand)
	if err != nil {
		t.Fatalf("Error push image: %s", output)
	}
}

// #3 It should create a new job
func TestCreatingNamespace(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Create namespace")
	fullCommand := fmt.Sprintf("kubectl create namespace %s", testId)
	t.Logf("Create new kubernetes namespace with command: `%s`", fullCommand)
	err, output := execCommand(fullCommand)
	if err != nil {
		failed = true
		t.Fatalf("Error creating namespace: %s", output)
	}
}

// #4 It should create a new job
func TestCreatingJob(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Apply kubernetes resources to test namespace")
	err, dstFilaname := copyFileContentsAndReplace("./test-fixtures/", "kubernetes-resources.yml", testId, imageName)
	if err != nil {
		failed = true
		t.Fatalf("Failed to execute file replacement", err.Error())
	}
	fullCommand := fmt.Sprintf("kubectl --namespace %s apply -f %s", testId, dstFilaname)
	t.Logf("Update kubernetes with command: `%s`", fullCommand)
	err, output := execCommand(fullCommand)
	if err != nil {
		failed = true
		t.Fatalf("Error applying job: %s", output)
	}
}

// #5 It should wait for a new IP address
func TestCreatingOfIp(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Start to watch for creation of IP address")
	fullCommand := fmt.Sprintf("kubectl --namespace=%s get svc %s -o json", testId, serviceName)
	for {
		time.Sleep(1000 * time.Millisecond)
		t.Log("Fetching IP address...")
		err, output := execCommand(fullCommand)
		if err != nil {
			continue
		}
		res := K8sServiceResponse{}
		err = json.Unmarshal([]byte(output), &res)
		if err != nil {
			continue
		}
		if len(res.Status.LoadBalancer.Ingress) == 0 {
			continue
		}
		if res.Status.LoadBalancer.Ingress[0].Ip == "" {
			continue
		}
		serviceIPaddress = res.Status.LoadBalancer.Ingress[0].Ip
		t.Log("IP address found: %s", serviceIPaddress)
		break
	}
}

// #6 It should create/update DNS entry in cloudflare with IP address
func TestCreatingOfDNSEntry(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Create DNS entry")
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", ZONE_ID)
	jsonStr := fmt.Sprintf("{\"type\":\"A\",\"name\":\"%s.%s\",\"content\": \"%s\"}", testId, DOMAIN, serviceIPaddress)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(jsonStr)))
	req.Header.Set("X-Auth-Email", CLOUDFLARE_EMAIL)
	req.Header.Set("X-Auth-Key", CLOUDFLARE_API_KEY)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		failed = true
		t.Fatalf("Error creating DNS entry: %s", err)
	}
	if resp.StatusCode != 200 {
		failed = true
		t.Fatalf("Error creating DNS entry (Status code is not 200): %s", resp.StatusCode)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		failed = true
		t.Fatalf("Error creating DNS entry: %s, %s", resp.StatusCode, err)
	}
	res := CloudflareZoneCreationResponse{}
	err = json.Unmarshal([]byte(body), &res)
	if err != nil {
		failed = true
		t.Fatalf("Error Marshalling JSON: %s", err)
	}
	if res.Result.Id == "" {
		failed = true
		t.Fatalf("Error getting ID for result: %s", res)
	}
	dnsRecordId = res.Result.Id
}

func TestDNSResolution(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Start checking for DNS resolution")
	url := fmt.Sprintf("%s.%s", testId, DOMAIN)
	for {
		time.Sleep(1000 * time.Millisecond)
		ips, err := net.LookupIP(url)
		if err != nil {
			t.Log("Error looking up IP for DNS entry: %s", err)
			continue
		}
		if ips[0].String() != serviceIPaddress {
			t.Log("Error looking up IP for DNS entry: %s", err)
			continue
		}
		break
	}
}

// #7 It should wait for the Service to be healthy
func TestHealth(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	t.Log("Start checking for health")
	url := fmt.Sprintf("http://%s.%s", testId, DOMAIN)
	// Be sure to manually clear your DNS cache when running this test
	for {
		time.Sleep(1000 * time.Millisecond)
		resp, err := http.Get(url)
		if err != nil || resp.StatusCode != 200 {
			t.Log("Error making HTTP response: %s", err)
			continue
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			failed = true
			t.Fatalf("Error Reading body: %s", err)
		}
		matched, _ := regexp.MatchString("Healthy.*true", string(body))
		if !matched {
			t.Log("HTTP response did not match")
			continue
		}
		t.Log("HTTP response matched")
		break
	}
}

// #8 It should have successfully completed the job
func TestJobCompletion(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	fullCommand := fmt.Sprintf("kubectl --namespace %s get job %s -o json", testId, JOB_NAME)
	for {
		err, output := execCommand(fullCommand)
		if err != nil {
			continue
		}
		res := K8sJobResponse{}
		err = json.Unmarshal([]byte(output), &res)
		if err != nil {
			continue
		}
		succeeded := res.Status.Succeeded
		if succeeded != 1 {
			continue
		}
		break
	}
}

// #9 It should have successfully added the registration field
func TestRegistrationCreation(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	fullCommand := fmt.Sprintf("kubectl --namespace %s get secret %s -o json", testId, REGISTRATION_SECRET_NAME)
	err, output := execCommand(fullCommand)
	if err != nil {
		failed = true
		t.Fatalf("Error getting secret: %s", err)
		return
	}
	res := K8sSecretResponse{}
	err = json.Unmarshal([]byte(output), &res)
	if err != nil {
		failed = true
		t.Fatalf("Error marshalling JSON: %s", err)
		return
	}
	if res.Data["registration"] == "" {
		failed = true
		t.Fatalf("Registration not found: %s", res.Data, output)
		return
	}
}

// #10 It should have successfully added the certs
func TestCertsFound(t *testing.T) {
	if failed {
		t.SkipNow()
	}
	fullCommand := fmt.Sprintf("kubectl --namespace %s get secret %s -o json", testId, CERT_SECRET_NAME)
	err, output := execCommand(fullCommand)
	if err != nil {
		failed = true
		t.Fatalf("Error getting secret: %s", err)
		return
	}
	res := K8sSecretResponse{}
	err = json.Unmarshal([]byte(output), &res)
	crt := res.Data[testId+"."+DOMAIN+".crt"]
	if err != nil || crt == "" {
		failed = true
		t.Fatalf("Error marshalling JSON: %s / %s", err, res.Data[testId+"."+DOMAIN+".crt"])
		return
	}
}

func tearDown() {
	if os.Getenv("NO_TEARDOWN") != "" {
		DeleteNamespace()
		DeleteFiles()
		DeleteDNSEntry()
	}
}

func DeleteNamespace() error {
	fullCommand := fmt.Sprintf("kubectl delete namespace %s", testId)
	log.Printf("Delete namespace with command: %s", fullCommand)
	err, output := execCommand(fullCommand)
	if err != nil {
		log.Fatal("Error deleting namespace: %s", output)
		return err
	}
	return nil
}

func DeleteFiles() error {
	files, err := ioutil.ReadDir(".")
	if err != nil {
		log.Fatal(err)
		return err
	}

	for _, file := range files {
		if strings.Contains(file.Name(), testId) {
			p := path.Join("./test-fixtures/", file.Name())
			log.Printf("Delete file: %s", p)
			os.Remove(p)
		}
	}
	return nil
}

func DeleteDNSEntry() error {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", ZONE_ID, dnsRecordId)
	log.Printf("Delete DNS entry: %s, %s, %s", ZONE_ID, dnsRecordId, url)
	req, err := http.NewRequest("DELETE", url, nil)
	req.Header.Set("X-Auth-Email", CLOUDFLARE_EMAIL)
	req.Header.Set("X-Auth-Key", CLOUDFLARE_API_KEY)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Error deleting DNS entry: %s, %s", resp.StatusCode, err)
		return err
	}
	defer resp.Body.Close()
	log.Printf("Status code: %s", resp.StatusCode)
	return nil
}

func TestMain(m *testing.M) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	testId = strconv.Itoa(int(r.Int31()))
	testNodePort = strconv.Itoa(time.Now().Second()%2767 + 30000)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			log.Print("Tests stopped. Tear down tests.")
			tearDown()
			os.Exit(1)
		}
	}()

	retCode := m.Run()
	tearDown()
	os.Exit(retCode)
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return fallback
	}
	return value
}

func execCommand(cmdString string) (error, string) {
	splitCmd := strings.Split(cmdString, " ")
	cmd := exec.Command(splitCmd[0], splitCmd[1:]...)
	var out, err bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &err
	cmdErr := cmd.Run()
	if cmdErr != nil {
		return cmdErr, err.String()
	}
	return nil, out.String()
}

func copyFileContentsAndReplace(dir string, fileName string, testId string, imageName string) (error, string) {
	src := path.Join(dir, fileName)
	newFilename := fmt.Sprintf("%s-%s", testId, fileName)
	dst := path.Join(dir, newFilename)
	read, err := ioutil.ReadFile(src)
	if err != nil {
		return err, dst
	}
	newContents := strings.Replace(string(read), "*IMAGE_NAME*", imageName, -1)
	newContents = strings.Replace(newContents, "*SUBDOMAIN*", testId, -1)
	newContents = strings.Replace(newContents, "*NODE_PORT*", testNodePort, -1)
	err = ioutil.WriteFile(dst, []byte(newContents), 0777)
	if err != nil {
		return err, dst
	}
	return nil, dst
}
