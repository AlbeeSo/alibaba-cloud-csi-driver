package dfs

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

// CreateMountPoint invokes the dfs.CreateMountPoint API synchronously
func (client *Client) CreateMountPoint(request *CreateMountPointRequest) (response *CreateMountPointResponse, err error) {
	response = CreateCreateMountPointResponse()
	err = client.DoAction(request, response)
	return
}

// CreateMountPointWithChan invokes the dfs.CreateMountPoint API asynchronously
func (client *Client) CreateMountPointWithChan(request *CreateMountPointRequest) (<-chan *CreateMountPointResponse, <-chan error) {
	responseChan := make(chan *CreateMountPointResponse, 1)
	errChan := make(chan error, 1)
	err := client.AddAsyncTask(func() {
		defer close(responseChan)
		defer close(errChan)
		response, err := client.CreateMountPoint(request)
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

// CreateMountPointWithCallback invokes the dfs.CreateMountPoint API asynchronously
func (client *Client) CreateMountPointWithCallback(request *CreateMountPointRequest, callback func(response *CreateMountPointResponse, err error)) <-chan int {
	result := make(chan int, 1)
	err := client.AddAsyncTask(func() {
		var response *CreateMountPointResponse
		var err error
		defer close(result)
		response, err = client.CreateMountPoint(request)
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

// CreateMountPointRequest is the request struct for api CreateMountPoint
type CreateMountPointRequest struct {
	*requests.RpcRequest
	Description   string `position:"Query" name:"Description"`
	NetworkType   string `position:"Query" name:"NetworkType"`
	AccessGroupId string `position:"Query" name:"AccessGroupId"`
	InputRegionId string `position:"Query" name:"InputRegionId"`
	FileSystemId  string `position:"Query" name:"FileSystemId"`
	VSwitchId     string `position:"Query" name:"VSwitchId"`
	VpcId         string `position:"Query" name:"VpcId"`
}

// CreateMountPointResponse is the response struct for api CreateMountPoint
type CreateMountPointResponse struct {
	*responses.BaseResponse
	MountPointId string `json:"MountPointId" xml:"MountPointId"`
	RequestId    string `json:"RequestId" xml:"RequestId"`
}

// CreateCreateMountPointRequest creates a request to invoke CreateMountPoint API
func CreateCreateMountPointRequest() (request *CreateMountPointRequest) {
	request = &CreateMountPointRequest{
		RpcRequest: &requests.RpcRequest{},
	}
	request.InitWithApiInfo("DFS", "2018-06-20", "CreateMountPoint", "alidfs", "openAPI")
	request.Method = requests.POST
	return
}

// CreateCreateMountPointResponse creates a response to parse from CreateMountPoint response
func CreateCreateMountPointResponse() (response *CreateMountPointResponse) {
	response = &CreateMountPointResponse{
		BaseResponse: &responses.BaseResponse{},
	}
	return
}