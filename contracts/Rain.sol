// SPDX-License-Identifier: MIT
pragma solidity 0.8.19;

interface IERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
}

interface IWZNN {
    function deposit() external payable;
    function withdraw(uint256 amount) external;
}

/// @title Rain
/// @notice Splits one person's tokens evenly between the people who were
/// talking in a Telegram group, on that person's command to the bot.
///
/// Everyone who takes part links first: `link(secret)` carries the one-time
/// number the bot handed them in a direct message, which is how the bot learns
/// which Telegram account the address belongs to. Nothing Telegram-side is
/// stored here; the contract only records that the address opted in.
///
/// A sender lets Rain spend by approving it on each token they want to rain,
/// for the amount they choose: `approve(rain, amount)` on that token alone.
/// Nothing is deposited and nothing else is reachable. ZNN, the chain's coin,
/// has no approve, so it is rained from wrapped ZNN (WZNN, 1:1): the sender
/// approves WZNN, and Rain unwraps it so recipients receive plain ZNN.
///
/// The operator is the bot's key and the only caller of `rain`. It chooses the
/// recipients, so it is trusted with what senders approve, as Bullet Game's
/// escrow owner is. What it cannot do is decided here: spend more than a
/// sender approved, pay anyone who has not linked, spend from anyone who has
/// not linked (or has since unlinked), pay one address twice in a rain, or pay
/// one rain twice.
contract Rain {
    /// @notice Address that stands for ZNN, the chain's coin, in `rain`.
    address public constant ZNN = address(0);
    /// @notice The most recipients one rain pays; it bounds the gas of a call.
    uint256 public constant MAX_RECIPIENTS = 100;
    /// @notice Gas forwarded with each ZNN payment. Enough for a plain wallet
    /// and a proxy's receive; a recipient that needs more is paid in WZNN.
    uint256 public constant PUSH_GAS = 30_000;

    /// @notice Wrapped ZNN, which ZNN rains are drawn from.
    address public immutable wznn;
    address public operator;
    mapping(address => bool) public linked;
    /// @notice Rain ids already paid. The bot numbers every rain before it
    /// sends it, so a resent transaction after a lost receipt pays nothing.
    mapping(uint64 => bool) public rained;

    uint256 private locked = 1;

    event Linked(address indexed account, uint64 secret);
    event Unlinked(address indexed account);
    event Rained(uint64 indexed id, address indexed from, address indexed token, uint256 each, uint256 recipients);
    /// @notice A ZNN share the recipient refused, paid to it in WZNN instead.
    event PaidWrapped(uint64 indexed id, address indexed account, uint256 amount);
    event OperatorChanged(address indexed previous, address indexed next);

    modifier onlyOperator() {
        require(msg.sender == operator, "not the operator");
        _;
    }

    modifier nonReentrant() {
        require(locked == 1, "reentrant call");
        locked = 2;
        _;
        locked = 1;
    }

    constructor(address operator_, address wznn_) {
        require(operator_ != address(0), "null operator");
        require(wznn_.code.length > 0, "WZNN is not a contract");
        operator = operator_;
        wznn = wznn_;
        emit OperatorChanged(address(0), operator_);
    }

    /// @dev Only WZNN pays ZNN in, when a ZNN rain unwraps it. Anything else
    /// sent here would be stranded.
    receive() external payable {
        require(msg.sender == wznn, "send nothing here; approve instead");
    }

    // --- everyone -----------------------------------------------------------

    /// @notice Opt in, carrying the secret the bot gave you.
    function link(uint64 secret) external {
        linked[msg.sender] = true;
        emit Linked(msg.sender, secret);
    }

    /// @notice Opt out. The bot can no longer rain from you or to you, whatever
    /// you have approved.
    function unlink() external {
        linked[msg.sender] = false;
        emit Unlinked(msg.sender);
    }

    // --- the bot ------------------------------------------------------------

    /// @notice Pays `each` of `token` from `from` to every address in `to`,
    /// within what `from` approved Rain for on that token (on WZNN for ZNN).
    /// `to` must be strictly ascending, which is how a duplicate is refused
    /// without a second loop.
    function rain(uint64 id, address from, address token, uint256 each, address[] calldata to)
        external
        onlyOperator
        nonReentrant
    {
        require(!rained[id], "rain already paid");
        rained[id] = true;
        require(linked[from], "sender not linked");
        uint256 n = to.length;
        require(n > 0 && n <= MAX_RECIPIENTS, "bad recipient count");
        require(each > 0, "nothing to rain");

        address prev = address(0);
        for (uint256 i = 0; i < n; i++) {
            address r = to[i];
            require(r > prev, "recipients not ascending");
            require(r != from, "sender among recipients");
            require(linked[r], "recipient not linked");
            prev = r;
        }

        if (token == ZNN) {
            uint256 total = each * n;
            _call(wznn, abi.encodeWithSelector(IERC20.transferFrom.selector, from, address(this), total));
            IWZNN(wznn).withdraw(total);
            for (uint256 i = 0; i < n; i++) {
                (bool ok, ) = to[i].call{value: each, gas: PUSH_GAS}("");
                if (!ok) {
                    // A contract that refuses ZNN still gets its share, wrapped.
                    IWZNN(wznn).deposit{value: each}();
                    _call(wznn, abi.encodeWithSelector(IERC20.transfer.selector, to[i], each));
                    emit PaidWrapped(id, to[i], each);
                }
            }
        } else {
            require(token.code.length > 0, "token is not a contract");
            for (uint256 i = 0; i < n; i++) {
                _call(token, abi.encodeWithSelector(IERC20.transferFrom.selector, from, to[i], each));
            }
        }
        emit Rained(id, from, token, each, n);
    }

    function setOperator(address next) external onlyOperator {
        require(next != address(0), "null operator");
        emit OperatorChanged(operator, next);
        operator = next;
    }

    /// @dev A token call that accepts tokens returning nothing, as USDT does,
    /// and refuses one that reverts or returns false.
    function _call(address token, bytes memory data) private {
        (bool ok, bytes memory ret) = token.call(data);
        require(ok && (ret.length == 0 || (ret.length >= 32 && abi.decode(ret, (bool)))), "token transfer failed");
    }
}
