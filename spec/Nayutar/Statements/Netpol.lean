import Nayutar.Netpol

set_option autoImplicit false

namespace Nayutar.Netpol

theorem invalid_conn_dropped (cp : CompiledPolicy) (pkt : Packet)
    (hinv : pkt.connState = .invalid) :
    evaluate cp pkt = .drop ∧ evalNft (compileNft cp) pkt = .drop := by
  sorry

theorem established_related_accepted (cp : CompiledPolicy) (pkt : Packet)
    (hest : pkt.connState = .established ∨ pkt.connState = .related) :
    evaluate cp pkt = .accept ∧ evalNft (compileNft cp) pkt = .accept := by
  sorry

theorem compile_tenants_cidrs_disjoint (bundle : PolicyBundle) (cp : CompiledPolicy)
    (hc : compile bundle = .ok cp) :
    (∀ t1 ∈ cp.tenants, ∀ t2 ∈ cp.tenants, t1.tenant ≠ t2.tenant →
      ∀ c1 ∈ t1.cidrs, ∀ c2 ∈ t2.cidrs, Prefix.overlaps c1 c2 = false) ∧
    (∀ fam addr, ∀ t1 ∈ cp.tenants, ∀ t2 ∈ cp.tenants,
      tenantContains t1 fam addr = true → tenantContains t2 fam addr = true →
      t1 = t2) := by
  sorry

theorem default_deny_cross_tenant_when_no_rules (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (hempty : ∀ t ∈ cp.tenants, t.rules = [])
    (hcross : ∀ t ∈ cp.tenants,
      !(tenantContains t pkt.family pkt.srcAddr && tenantContains t pkt.family pkt.dstAddr)) :
    evaluate cp pkt = .drop := by
  sorry

theorem intra_tenant_allow_mode_accepts (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet) (t : TenantPolicy)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (ht : t ∈ cp.tenants)
    (hallow : t.intraTenant = .allow)
    (hsrc : tenantContains t pkt.family pkt.srcAddr = true)
    (hdst : tenantContains t pkt.family pkt.dstAddr = true) :
    evaluate cp pkt = .accept := by
  sorry

theorem cross_tenant_requires_mutual_allow (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet) (srcT dstT : TenantPolicy)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (hs : srcT ∈ cp.tenants) (hd : dstT ∈ cp.tenants)
    (hne : srcT.tenant ≠ dstT.tenant)
    (hsrc : tenantContains srcT pkt.family pkt.srcAddr = true)
    (hdst : tenantContains dstT pkt.family pkt.dstAddr = true) :
    evaluate cp pkt = .accept ↔
      (matchesTenantRules cp srcT .egress pkt = true ∧
       matchesTenantRules cp dstT .ingress pkt = true) := by
  sorry

theorem external_transit_always_dropped (cp : CompiledPolicy) (pkt : Packet)
    (hnew : pkt.connState = .new)
    (hsrc : findTenant cp pkt.family pkt.srcAddr = none)
    (hdst : findTenant cp pkt.family pkt.dstAddr = none) :
    evaluate cp pkt = .drop := by
  sorry

theorem chain_names_disjoint (t1 t2 : String)
    (h1 : isValidTenantName t1 = true) (h2 : isValidTenantName t2 = true) :
    tenantChainName t1 ≠ tenantInChainName t2 ∧
    tenantChainName t1 ≠ forwardChainName ∧
    tenantInChainName t1 ≠ forwardChainName ∧
    (tenantChainName t1 = tenantChainName t2 → t1 = t2) ∧
    (tenantInChainName t1 = tenantInChainName t2 → t1 = t2) := by
  sorry

theorem compiled_nft_matches_evaluate (bundle : PolicyBundle) (cp : CompiledPolicy) (pkt : Packet)
    (hc : compile bundle = .ok cp) :
    evalNft (compileNft cp) pkt = evaluate cp pkt := by
  sorry

theorem compile_order_independent (b1 b2 : PolicyBundle) (cp1 cp2 : CompiledPolicy)
    (h1 : compile b1 = .ok cp1) (h2 : compile b2 = .ok cp2)
    (hperm : b1.tenants.Perm b2.tenants) :
    cp1 = cp2 ∧ compileNft cp1 = compileNft cp2 := by
  sorry

end Nayutar.Netpol
