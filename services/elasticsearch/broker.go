package elasticsearch

import (
	"encoding/json"
	"net/http"

	"github.com/jinzhu/gorm"

	"github.com/18F/aws-broker/base"
	"github.com/18F/aws-broker/catalog"
	"github.com/18F/aws-broker/config"
	"github.com/18F/aws-broker/helpers/request"
	"github.com/18F/aws-broker/helpers/response"
)

type ElasticsearchOptions struct {
	ElasticsearchVersion string `json:"elasticsearchVersion"`
	Bucket               string `json:"bucket"`
}

func (r ElasticsearchOptions) Validate(settings *config.Settings) error {
	return nil
}

type elasticsearchBroker struct {
	brokerDB *gorm.DB
	settings *config.Settings
}

// InitelasticsearchBroker is the constructor for the elasticsearchBroker.
func InitElasticsearchBroker(brokerDB *gorm.DB, settings *config.Settings) base.Broker {
	return &elasticsearchBroker{brokerDB, settings}
}

// initializeAdapter is the main function to create database instances
func initializeAdapter(plan catalog.ElasticsearchPlan, s *config.Settings, c *catalog.Catalog) (ElasticsearchAdapter, response.Response) {

	var elasticsearchAdapter ElasticsearchAdapter
	if s.Environment == "test" {
		elasticsearchAdapter = &mockElasticsearchAdapter{}
		return elasticsearchAdapter, nil
	}

	elasticsearchAdapter = &dedicatedElasticsearchAdapter{
		Plan:     plan,
		settings: *s,
	}
	return elasticsearchAdapter, nil
}

func (broker *elasticsearchBroker) CreateInstance(c *catalog.Catalog, id string, createRequest request.Request) response.Response {
	newInstance := ElasticsearchInstance{}

	options := ElasticsearchOptions{}
	if len(createRequest.RawParameters) > 0 {
		err := json.Unmarshal(createRequest.RawParameters, &options)
		if err != nil {
			return response.NewErrorResponse(http.StatusBadRequest, "Invalid parameters. Error: "+err.Error())
		}
		err = options.Validate(broker.settings)
		if err != nil {
			return response.NewErrorResponse(http.StatusBadRequest, "Invalid parameters. Error: "+err.Error())
		}
	}

	var count int64
	broker.brokerDB.Where("uuid = ?", id).First(&newInstance).Count(&count)
	if count != 0 {
		return response.NewErrorResponse(http.StatusConflict, "The instance already exists")
	}

	plan, planErr := c.ElasticsearchService.FetchPlan(createRequest.PlanID)
	if planErr != nil {
		return planErr
	}

	err := newInstance.init(
		id,
		createRequest.OrganizationGUID,
		createRequest.SpaceGUID,
		createRequest.ServiceID,
		plan,
		options,
		broker.settings)

	if err != nil {
		return response.NewErrorResponse(http.StatusBadRequest, "There was an error initializing the instance. Error: "+err.Error())
	}

	adapter, adapterErr := initializeAdapter(plan, broker.settings, c)
	if adapterErr != nil {
		return adapterErr
	}
	// Create the elasticsearch instance.
	status, err := adapter.createElasticsearch(&newInstance, newInstance.ClearPassword)
	if status == base.InstanceNotCreated {
		desc := "There was an error creating the instance."
		if err != nil {
			desc = desc + " Error: " + err.Error()
		}
		return response.NewErrorResponse(http.StatusBadRequest, desc)
	}

	newInstance.State = status
	broker.brokerDB.NewRecord(newInstance)
	err = broker.brokerDB.Create(&newInstance).Error
	if err != nil {
		return response.NewErrorResponse(http.StatusBadRequest, err.Error())
	}
	return response.SuccessAcceptedResponse
}

func (broker *elasticsearchBroker) ModifyInstance(c *catalog.Catalog, id string, updateRequest request.Request, baseInstance base.Instance) response.Response {
	// Note:  This is not currently supported for Redis instances.
	return response.NewErrorResponse(http.StatusBadRequest, "Updating Elasticsearch service instances is not supported at this time.")
}

func (broker *elasticsearchBroker) LastOperation(c *catalog.Catalog, id string, baseInstance base.Instance) response.Response {
	existingInstance := ElasticsearchInstance{}

	var count int64
	broker.brokerDB.Where("uuid = ?", id).First(&existingInstance).Count(&count)
	if count == 0 {
		return response.NewErrorResponse(http.StatusNotFound, "Instance not found")
	}

	plan, planErr := c.ElasticsearchService.FetchPlan(baseInstance.PlanID)
	if planErr != nil {
		return planErr
	}

	adapter, adapterErr := initializeAdapter(plan, broker.settings, c)
	if adapterErr != nil {
		return adapterErr
	}

	var state string

	status, _ := adapter.checkElasticsearchStatus(&existingInstance)
	switch status {
	case base.InstanceInProgress:
		state = "in progress"
	case base.InstanceReady:
		state = "succeeded"
	case base.InstanceNotCreated:
		state = "failed"
	case base.InstanceNotGone:
		state = "failed"
	default:
		state = "in progress"
	}
	return response.NewSuccessLastOperation(state, "The service instance status is "+state)
}

func (broker *elasticsearchBroker) BindInstance(c *catalog.Catalog, id string, bindRequest request.Request, baseInstance base.Instance) response.Response {
	existingInstance := ElasticsearchInstance{}

	options := ElasticsearchOptions{}
	if len(bindRequest.RawParameters) > 0 {
		err := json.Unmarshal(bindRequest.RawParameters, &options)
		if err != nil {
			return response.NewErrorResponse(http.StatusBadRequest, "Invalid parameters. Error: "+err.Error())
		}
		err = options.Validate(broker.settings)
		if err != nil {
			return response.NewErrorResponse(http.StatusBadRequest, "Invalid parameters. Error: "+err.Error())
		}
	}

	var count int64
	broker.brokerDB.Where("uuid = ?", id).First(&existingInstance).Count(&count)
	if count == 0 {
		return response.NewErrorResponse(http.StatusNotFound, "Instance not found")
	}

	plan, planErr := c.ElasticsearchService.FetchPlan(baseInstance.PlanID)
	if planErr != nil {
		return planErr
	}

	password, err := existingInstance.getPassword(broker.settings.EncryptionKey)
	if err != nil {
		return response.NewErrorResponse(http.StatusInternalServerError, "Unable to get instance password.")
	}

	// Get the correct database logic depending on the type of plan. (shared vs dedicated)
	adapter, adapterErr := initializeAdapter(plan, broker.settings, c)
	if adapterErr != nil {
		return adapterErr
	}

	var credentials map[string]string
	// Bind the database instance to the application.
	originalInstanceState := existingInstance.State
	existingInstance.setBucket(options.Bucket)
	if credentials, err = adapter.bindElasticsearchToApp(&existingInstance, password); err != nil {
		desc := "There was an error binding the database instance to the application."
		if err != nil {
			desc = desc + " Error: " + err.Error()
		}
		return response.NewErrorResponse(http.StatusBadRequest, desc)
	}

	if len(existingInstance.Bucket) > 0 {
		broker.brokerDB.Save(&existingInstance)
	}
	// If the state of the instance has changed, update it.
	if existingInstance.State != originalInstanceState {
		broker.brokerDB.Save(&existingInstance)
	}

	return response.NewSuccessBindResponse(credentials)
}

func (broker *elasticsearchBroker) DeleteInstance(c *catalog.Catalog, id string, baseInstance base.Instance) response.Response {
	existingInstance := ElasticsearchInstance{}
	var count int64
	broker.brokerDB.Where("uuid = ?", id).First(&existingInstance).Count(&count)
	if count == 0 {
		return response.NewErrorResponse(http.StatusNotFound, "Instance not found")
	}

	plan, planErr := c.ElasticsearchService.FetchPlan(baseInstance.PlanID)
	if planErr != nil {
		return planErr
	}

	adapter, adapterErr := initializeAdapter(plan, broker.settings, c)
	if adapterErr != nil {
		return adapterErr
	}
	// Delete the database instance.
	if status, err := adapter.deleteElasticsearch(&existingInstance); status == base.InstanceNotGone {
		desc := "There was an error deleting the instance."
		if err != nil {
			desc = desc + " Error: " + err.Error()
		}
		return response.NewErrorResponse(http.StatusBadRequest, desc)
	}
	broker.brokerDB.Unscoped().Delete(&existingInstance)
	return response.SuccessDeleteResponse
}
