package tokenrecon

import log "github.com/sirupsen/logrus"

// erc20GeneralSpecs returns call specs for the ERC20 "general" rule.
// Covers: CANCEL_AUTHORIZATION_TYPEHASH, DOMAIN_SEPARATOR, PERMIT_TYPEHASH,
// RECEIVE_WITH_AUTHORIZATION_TYPEHASH, TRANSFER_WITH_AUTHORIZATION_TYPEHASH,
// currency, decimals, name, symbol, totalSupply, version.
func erc20GeneralSpecs() []callSpec {
	return []callSpec{
		{field: "CANCEL_AUTHORIZATION_TYPEHASH", calldata: fnCalldata("CANCEL_AUTHORIZATION_TYPEHASH()")},
		{field: "DOMAIN_SEPARATOR", calldata: fnCalldata("DOMAIN_SEPARATOR()")},
		{field: "PERMIT_TYPEHASH", calldata: fnCalldata("PERMIT_TYPEHASH()")},
		{field: "RECEIVE_WITH_AUTHORIZATION_TYPEHASH", calldata: fnCalldata("RECEIVE_WITH_AUTHORIZATION_TYPEHASH()")},
		{field: "TRANSFER_WITH_AUTHORIZATION_TYPEHASH", calldata: fnCalldata("TRANSFER_WITH_AUTHORIZATION_TYPEHASH()")},
		{field: "currency", calldata: fnCalldata("currency()")},
		{field: "decimals", calldata: fnCalldata("decimals()")},
		{field: "name", calldata: fnCalldata("name()")},
		{field: "symbol", calldata: fnCalldata("symbol()")},
		{field: "totalSupply", calldata: fnCalldata("totalSupply()")},
		{field: "version", calldata: fnCalldata("version()")},
	}
}

// erc20BalanceSpecs returns call specs for balanceOf and nonces for a given address.
func erc20BalanceSpecs(addr string) ([]callSpec, error) {
	balCD, err := fnCalldataAddr("balanceOf(address)", addr)
	if err != nil {
		return nil, err
	}
	noncesCD, err := fnCalldataAddr("nonces(address)", addr)
	if err != nil {
		return nil, err
	}
	return []callSpec{
		{field: "balanceOf:" + addr, calldata: balCD},
		{field: "nonces:" + addr, calldata: noncesCD},
	}, nil
}

// erc20RolesSpecs returns call specs for the ERC20 "roles" rule.
// Covers: blacklister, masterMinter, owner, pauser, rescuer.
func erc20RolesSpecs() []callSpec {
	return []callSpec{
		{field: "blacklister", calldata: fnCalldata("blacklister()")},
		{field: "masterMinter", calldata: fnCalldata("masterMinter()")},
		{field: "owner", calldata: fnCalldata("owner()")},
		{field: "pauser", calldata: fnCalldata("pauser()")},
		{field: "rescuer", calldata: fnCalldata("rescuer()")},
	}
}

// erc721GeneralSpecs returns call specs for the ERC721 "general" rule.
// Covers: name, paused, symbol.
func erc721GeneralSpecs() []callSpec {
	return []callSpec{
		{field: "name", calldata: fnCalldata("name()")},
		{field: "paused", calldata: fnCalldata("paused()")},
		{field: "symbol", calldata: fnCalldata("symbol()")},
	}
}

// erc721BalanceSpecs returns call specs for balanceOf for a given address.
func erc721BalanceSpecs(addr string) ([]callSpec, error) {
	balCD, err := fnCalldataAddr("balanceOf(address)", addr)
	if err != nil {
		return nil, err
	}
	return []callSpec{
		{field: "balanceOf:" + addr, calldata: balCD},
	}, nil
}

// buildERC20Specs assembles all call specs for the enabled rules of an ERC20 token.
func buildERC20Specs(token *TokenConfig) []callSpec {
	var specs []callSpec
	for _, rule := range token.Rules {
		switch rule {
		case "general":
			specs = append(specs, erc20GeneralSpecs()...)
		case "balance":
			specs = append(specs, buildAddrSpecs(token.Addresses, erc20BalanceSpecs)...)
		case "roles":
			specs = append(specs, erc20RolesSpecs()...)
		case "codehash", "storagehash":
			// handled via eth_getProof in fetchAndCompareProof
		default:
			log.WithField("rule", rule).Warn("unknown rule in erc20 token config, skipping")
		}
	}
	return specs
}

// buildERC721Specs assembles all call specs for the enabled rules of an ERC721 token.
func buildERC721Specs(token *TokenConfig) []callSpec {
	var specs []callSpec
	for _, rule := range token.Rules {
		switch rule {
		case "general":
			specs = append(specs, erc721GeneralSpecs()...)
		case "balance":
			specs = append(specs, buildAddrSpecs(token.Addresses, erc721BalanceSpecs)...)
		case "codehash", "storagehash":
			// handled via eth_getProof in fetchAndCompareProof
		default:
			log.WithField("rule", rule).Warn("unknown rule in erc721 token config, skipping")
		}
	}
	return specs
}

type addrSpecsFn func(addr string) ([]callSpec, error)

func buildAddrSpecs(addresses []string, fn addrSpecsFn) []callSpec {
	var specs []callSpec
	for _, addr := range addresses {
		s, err := fn(addr)
		if err != nil {
			log.WithError(err).WithField("address", addr).Warn("skip address: invalid format")
			continue
		}
		specs = append(specs, s...)
	}
	return specs
}
