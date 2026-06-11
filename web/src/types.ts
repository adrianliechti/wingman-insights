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
  provider_name?: string
  request_model?: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  input_cost: number
  output_cost: number
  cache_read_cost: number
  cache_creation_cost: number
  total_cost: number
  priced: boolean
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

export interface MethodDistributionRow {
  method: string
  total_requests: number
}
