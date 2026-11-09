	// An empty predictive scaling configuration is none: a policy that is
	// not predictive carries none on AWS, and moto returns an empty one.
	if ko.Spec.PredictiveScalingConfiguration != nil && ko.Spec.PredictiveScalingConfiguration.MetricSpecifications == nil &&
		ko.Spec.PredictiveScalingConfiguration.Mode == nil && ko.Spec.PredictiveScalingConfiguration.MaxCapacityBreachBehavior == nil &&
		ko.Spec.PredictiveScalingConfiguration.MaxCapacityBuffer == nil && ko.Spec.PredictiveScalingConfiguration.SchedulingBufferTime == nil {
		ko.Spec.PredictiveScalingConfiguration = nil
	}
