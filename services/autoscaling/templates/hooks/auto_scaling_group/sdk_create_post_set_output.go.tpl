if ko.Spec.Tags != nil {
    ackcondition.SetSynced(&resource{ko}, corev1.ConditionFalse, nil, nil)
}
if len(ko.Spec.EnabledMetrics) > 0 {
    if err := syncEnabledMetrics(ctx, ko.Spec.EnabledMetrics, nil, aws.ToString(ko.Spec.Name), rm.sdkapi, rm.metrics); err != nil {
        return nil, err
    }
}
if len(ko.Spec.SuspendedProcesses) > 0 {
    if err := syncSuspendedProcesses(ctx, ko.Spec.SuspendedProcesses, nil, aws.ToString(ko.Spec.Name), rm.sdkapi, rm.metrics); err != nil {
        return nil, err
    }
}
