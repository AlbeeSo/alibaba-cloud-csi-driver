package nas

//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//http://www.apache.org/licenses/LICENSE-2.0
//
//Unless required by applicable law or agreed to in writing, software
//distributed under the License is distributed on an "AS IS" BASIS,
//WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//See the License for the specific language governing permissions and
//limitations under the License.
//
// Code generated by Alibaba Cloud SDK Code Generator.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

import (
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
)

// CancelDataFlowAutoRefresh invokes the nas.CancelDataFlowAutoRefresh API synchronously
func (client *Client) CancelDataFlowAutoRefresh(request *CancelDataFlowAutoRefreshRequest) (response *CancelDataFlowAutoRefreshResponse, err error) {
	response = CreateCancelDataFlowAutoRefreshResponse()
	err = client.DoAction(request, response)
	return
}

// CancelDataFlowAutoRefreshWithChan invokes the nas.CancelDataFlowAutoRefresh API asynchronously
func (client *Client) CancelDataFlowAutoRefreshWithChan(request *CancelDataFlowAutoRefreshRequest) (<-chan *CancelDataFlowAutoRefreshResponse, <-chan error) {
	responseChan := make(chan *CancelDataFlowAutoRefreshResponse, 1)
	errChan := make(chan error, 1)
	err := client.AddAsyncTask(func() {
		defer close(responseChan)
		defer close(errChan)
		response, err := client.CancelDataFlowAutoRefresh(request)
		if err != nil {
			errChan <- err
		} else {
			responseChan <- response
		}
	})
	if err != nil {
		errChan <- err
		close(responseChan)
		close(errChan)
	}
	return responseChan, errChan
}

// CancelDataFlowAutoRefreshWithCallback invokes the nas.CancelDataFlowAutoRefresh API asynchronously
func (client *Client) CancelDataFlowAutoRefreshWithCallback(request *CancelDataFlowAutoRefreshRequest, callback func(response *CancelDataFlowAutoRefreshResponse, err error)) <-chan int {
	result := make(chan int, 1)
	err := client.AddAsyncTask(func() {
		var response *CancelDataFlowAutoRefreshResponse
		var err error
		defer close(result)
		response, err = client.CancelDataFlowAutoRefresh(request)
		callback(response, err)
		result <- 1
	})
	if err != nil {
		defer close(result)
		callback(nil, err)
		result <- 0
	}
	return result
}

// CancelDataFlowAutoRefreshRequest is the request struct for api CancelDataFlowAutoRefresh
type CancelDataFlowAutoRefreshRequest struct {
	*requests.RpcRequest
	ClientToken  string           `position:"Query" name:"ClientToken"`
	RefreshPath  string           `position:"Query" name:"RefreshPath"`
	FileSystemId string           `position:"Query" name:"FileSystemId"`
	DryRun       requests.Boolean `position:"Query" name:"DryRun"`
	DataFlowId   string           `position:"Query" name:"DataFlowId"`
}

// CancelDataFlowAutoRefreshResponse is the response struct for api CancelDataFlowAutoRefresh
type CancelDataFlowAutoRefreshResponse struct {
	*responses.BaseResponse
	RequestId string `json:"RequestId" xml:"RequestId"`
}

// CreateCancelDataFlowAutoRefreshRequest creates a request to invoke CancelDataFlowAutoRefresh API
func CreateCancelDataFlowAutoRefreshRequest() (request *CancelDataFlowAutoRefreshRequest) {
	request = &CancelDataFlowAutoRefreshRequest{
		RpcRequest: &requests.RpcRequest{},
	}
	request.InitWithApiInfo("NAS", "2017-06-26", "CancelDataFlowAutoRefresh", "nas", "openAPI")
	request.Method = requests.POST
	return
}

// CreateCancelDataFlowAutoRefreshResponse creates a response to parse from CancelDataFlowAutoRefresh response
func CreateCancelDataFlowAutoRefreshResponse() (response *CancelDataFlowAutoRefreshResponse) {
	response = &CancelDataFlowAutoRefreshResponse{
		BaseResponse: &responses.BaseResponse{},
	}
	return
}
