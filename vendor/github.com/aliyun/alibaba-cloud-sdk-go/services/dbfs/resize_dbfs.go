package dbfs

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

// ResizeDbfs invokes the dbfs.ResizeDbfs API synchronously
func (client *Client) ResizeDbfs(request *ResizeDbfsRequest) (response *ResizeDbfsResponse, err error) {
	response = CreateResizeDbfsResponse()
	err = client.DoAction(request, response)
	return
}

// ResizeDbfsWithChan invokes the dbfs.ResizeDbfs API asynchronously
func (client *Client) ResizeDbfsWithChan(request *ResizeDbfsRequest) (<-chan *ResizeDbfsResponse, <-chan error) {
	responseChan := make(chan *ResizeDbfsResponse, 1)
	errChan := make(chan error, 1)
	err := client.AddAsyncTask(func() {
		defer close(responseChan)
		defer close(errChan)
		response, err := client.ResizeDbfs(request)
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

// ResizeDbfsWithCallback invokes the dbfs.ResizeDbfs API asynchronously
func (client *Client) ResizeDbfsWithCallback(request *ResizeDbfsRequest, callback func(response *ResizeDbfsResponse, err error)) <-chan int {
	result := make(chan int, 1)
	err := client.AddAsyncTask(func() {
		var response *ResizeDbfsResponse
		var err error
		defer close(result)
		response, err = client.ResizeDbfs(request)
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

// ResizeDbfsRequest is the request struct for api ResizeDbfs
type ResizeDbfsRequest struct {
	*requests.RpcRequest
	NewSizeG requests.Integer `position:"Query" name:"NewSizeG"`
	FsId     string           `position:"Query" name:"FsId"`
}

// ResizeDbfsResponse is the response struct for api ResizeDbfs
type ResizeDbfsResponse struct {
	*responses.BaseResponse
	RequestId string `json:"RequestId" xml:"RequestId"`
}

// CreateResizeDbfsRequest creates a request to invoke ResizeDbfs API
func CreateResizeDbfsRequest() (request *ResizeDbfsRequest) {
	request = &ResizeDbfsRequest{
		RpcRequest: &requests.RpcRequest{},
	}
	request.InitWithApiInfo("DBFS", "2020-04-18", "ResizeDbfs", "dbfs", "openAPI")
	request.Method = requests.POST
	return
}

// CreateResizeDbfsResponse creates a response to parse from ResizeDbfs response
func CreateResizeDbfsResponse() (response *ResizeDbfsResponse) {
	response = &ResizeDbfsResponse{
		BaseResponse: &responses.BaseResponse{},
	}
	return
}
