export interface TokenSummaryRow {
  provider_name: string
  request_model: string
  token_type: string
  total_tokens: number
  total_requests: number
}

export interface TimeseriesPoint {
  bucket: string
  value: number
  count: number
  label?: string
}

export interface OperationRow {
  operation_name: string
  request_model: string
  provider_name: string
  total_count: number
  avg_duration: number
}

export interface UserTokenSummaryRow {
  enduser_id: string
  enduser_email: string
  request_model: string
  token_type: string
  total_tokens: number
  total_requests: number
}

export interface ActiveUsersRow {
  dau: number
  wau: number
  mau: number
}

export interface TopConsumerRow {
  enduser_id: string
  enduser_email: string
  total_requests: number
  total_tokens: number
  tpm: number
}

export interface ModelDistributionRow {
  provider_name: string
  request_model: string
  total_requests: number
}

export interface GenAIErrorRow {
  error_type: string
  request_model: string
  count: number
}

export interface AnomalyPoint {
  bucket: string
  group_key: string
  token_type: string
  tokens: number
  expected: number
  score: number
}

export interface CostRow {
  enduser_id?: string
  enduser_email?: string
  service_name?: string
  provider_name?: string
  request_model?: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  input_cost: number
  output_cost: number
  cache_read_cost: number
  cache_creation_cost: number
  total_cost: number
  cache_savings: number
  priced: boolean
}

export interface SessionStats {
  sessions: number
  avg_per_user: number
  avg_tokens_per_session: number
}

export interface ToolStatRow {
  tool_name: string
  count: number
  avg_duration: number
  error_count: number
}

export interface BudgetResponse {
  budget: number
  month_to_date: number
  projected: number
  month_elapsed: number
}


export interface FilterUser {
  id: string
  email: string
}

export interface FilterOptions {
  services: string[]
  users: FilterUser[]
  providers: string[]
  models: string[]
}

export interface SpanRow {
  time: string
  duration: number
  trace_id: string
  span_id: string
  parent_span_id?: string
  name: string
  kind?: string
  status?: string
  service_name?: string
  operation_name?: string
  provider_name?: string
  request_model?: string
  response_model?: string
  agent_name?: string
  tool_name?: string
  user_id?: string
  user_email?: string
  session_id?: string
  error_type?: string
  finish_reasons?: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  cost: number
  attributes?: Record<string, string>
}

export interface TraceSummary {
  trace_id: string
  name: string
  time: string
  duration: number
  service_name?: string
  user_id?: string
  user_email?: string
  session_id?: string
  span_count: number
  input_tokens: number
  output_tokens: number
  cost: number
  has_error: boolean
}

export interface HTTPSummaryRow {
  direction: string
  method: string
  route: string
  total_requests: number
  avg_duration: number
  error_count: number
}

export interface HTTPErrorsByCodeRow {
  status_code: number
  count: number
}

export interface TopRouteRow {
  method: string
  route: string
  total_requests: number
}

// ScorePoint is one bucket of a single series scored against its rolling
// baseline (cost anomalies, generic spike charts).
export interface ScorePoint {
  bucket: string
  group_key: string
  value: number
  expected: number
  score: number
}

// AnomalyFeedRow is one flagged (dimension, entity, metric) bucket in the
// cross-dimension anomaly feed.
export interface AnomalyFeedRow {
  bucket: string
  dimension: string // user | service | model
  group_key: string
  metric: string // cost | tokens
  value: number
  expected: number
  score: number
}

export interface UserStatRow {
  enduser_id: string
  enduser_email: string
  requests: number
  tokens: number
  cost: number
  active_days: number
  top_model: string
  segment: string
}

export interface UserSegmentRow {
  segment: string
  users: number
  requests: number
  tokens: number
  cost: number
}

export interface CohortCell {
  cohort: string
  week_offset: number
  active: number
}

export interface AppAdoptionRow {
  service_name: string
  users: number
  requests: number
  tokens: number
  cost: number
}

export interface ModelPreferenceRow {
  segment: string
  model: string
  tokens: number
}

export interface BurstRow {
  enduser_id: string
  enduser_email: string
  peak_rpm: number
  total_requests: number
  active_minutes: number
}

